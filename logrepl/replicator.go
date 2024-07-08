package logrepl

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
)

type LogicalReplicator struct {
	Syncer                Syncer
	OutputPlugin          string
	ConnectionString      string
	PublicationName       string
	SlotName              string
	StandbyMessageTimeout time.Duration
	Schema                *Schema
	conn                  *pgconn.PgConn
	typeMap               *pgtype.Map
	state                 *replicationState
	slotCreationInfo      *pglogrepl.CreateReplicationSlotResult
	queryBuilder          *QueryBuilder
	transformer           *Transformer
}

type replicationState struct {
	nextStandbyMessageDeadline time.Time
	defaultStartingPos         pglogrepl.LSN
	lastWrittenLSN             pglogrepl.LSN
	lastReceivedLSN            pglogrepl.LSN
	currentTransactionLSN      pglogrepl.LSN
	relations                  map[uint32]*pglogrepl.RelationMessageV2
	inStream                   bool
	processMessages            bool
}

type replicationSlot struct {
	active             bool
	latestFlushedLsn   pglogrepl.LSN
	restartLsn         pglogrepl.LSN
	catalogXmin        uint32
	hasValidFlushedLsn bool
}

func (r *LogicalReplicator) Run() {
	conn, err := pgconn.Connect(context.Background(), r.ConnectionString)
	if err != nil {
		log.Fatalln(err)
	}
	defer conn.Close(context.Background())

	r.conn = conn
	r.typeMap = pgtype.NewMap()
	r.state = &replicationState{
		relations: map[uint32]*pglogrepl.RelationMessageV2{},
	}
	r.queryBuilder = NewQueryBuilder(strings.Split(r.ConnectionString, "?")[0], r.Schema)
	r.transformer = NewTransformer(r.Schema)

	err = r.initSyncer()
	if err != nil {
		log.Fatal("Failed init syncer: ", err)
	}

	err = r.initPublication()
	if err != nil {
		log.Fatalln("Failed init publication:", err)
	}

	err = r.initReplicationSlot()
	if err != nil {
		log.Fatalln("Failed init replication slot:", err)
	}

	pluginArguments := []string{
		"proto_version '2'",
		fmt.Sprintf("publication_names '%s'", r.PublicationName),
		"messages 'true'",
		"streaming 'true'",
	}

	// The LSN is the position in the WAL where we want to start replication, but it can only be used to skip entries,
	// not rewind to previous entries that we've already confirmed to the primary that we flushed. We still pass an LSN
	// for the edge case where we have flushed an entry to disk, but crashed before the primary received confirmation.
	// In that edge case, we want to "skip" entries (from the primary's perspective) that we have already flushed to disk.
	log.Printf("Starting logical replication on slot %s at WAL location %s", r.SlotName, r.state.defaultStartingPos+1)
	err = pglogrepl.StartReplication(
		context.Background(),
		conn,
		r.SlotName,
		r.state.defaultStartingPos,
		pglogrepl.StartReplicationOptions{
			PluginArgs: pluginArguments,
		})
	if err != nil {
		log.Fatalln("StartReplication failed: ", err)
	}

	r.state.lastWrittenLSN = r.state.defaultStartingPos

	for {
		if time.Now().After(r.state.nextStandbyMessageDeadline) {
			err = pglogrepl.SendStandbyStatusUpdate(context.Background(), conn, pglogrepl.StandbyStatusUpdate{
				WALWritePosition: r.state.lastWrittenLSN + 1,
				WALFlushPosition: r.state.lastWrittenLSN + 1,
				WALApplyPosition: r.state.lastReceivedLSN + 1,
			})
			if err != nil {
				log.Fatalln("SendStandbyStatusUpdate failed: ", err)
			}
			log.Printf("Sent Standby status message at %s\n", (r.state.lastWrittenLSN + 1).String())
			r.state.nextStandbyMessageDeadline = time.Now().Add(r.StandbyMessageTimeout)
		}

		ctx, cancel := context.WithDeadline(context.Background(), r.state.nextStandbyMessageDeadline)
		rawMsg, err := conn.ReceiveMessage(ctx)
		cancel()
		if err != nil {
			if pgconn.Timeout(err) {
				continue
			}
			log.Fatalln("ReceiveMessage failed:", err)
		}

		if errMsg, ok := rawMsg.(*pgproto3.ErrorResponse); ok {
			log.Fatalf("received Postgres WAL error: %+v", errMsg)
		}

		msg, ok := rawMsg.(*pgproto3.CopyData)
		if !ok {
			log.Printf("Received unexpected message: %T\n", rawMsg)
			continue
		}

		switch msg.Data[0] {
		case pglogrepl.PrimaryKeepaliveMessageByteID:
			pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
			if err != nil {
				log.Fatalln("ParsePrimaryKeepaliveMessage failed: ", err)
			}
			log.Println("Primary Keepalive Message =>", "ServerWALEnd:", pkm.ServerWALEnd, "ServerTime:", pkm.ServerTime, "ReplyRequested:", pkm.ReplyRequested)
			if pkm.ServerWALEnd > r.state.lastReceivedLSN {
				r.state.lastReceivedLSN = pkm.ServerWALEnd
			}
			if pkm.ReplyRequested {
				r.state.nextStandbyMessageDeadline = time.Time{}
			}

		case pglogrepl.XLogDataByteID:
			xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
			if err != nil {
				log.Fatalln("ParseXLogData failed: ", err)
			}

			log.Printf("XLogData => WALStart %s ServerWALEnd %s ServerTime %s WALData:\n", xld.WALStart, xld.ServerWALEnd, xld.ServerTime)

			committed, err := r.processMessage(xld)
			if err != nil {
				log.Println("Error processing message:", err)
			}

			if committed {
				r.state.lastWrittenLSN = r.state.currentTransactionLSN
				log.Printf("Writing LSN %s to file\n", r.state.lastWrittenLSN.String())
				err := r.writeWALPosition(r.state.lastWrittenLSN)
				if err != nil {
					log.Println(err)
				}
			}
		}
	}
}

func (r *LogicalReplicator) initSyncer() error {
	err := r.Syncer.OnInit(r.Schema)
	if err != nil {
		return err
	}

	log.Printf("Trying to do first-time full replication of %v tables", len(r.Schema.Nodes))

	for table, node := range r.Schema.Nodes {
		if node.Sync == SYNC_NONE {
			continue
		}

		log.Println("Replicating table:", table)

		rows, err := r.queryBuilder.GetRows(context.Background(), table, node.PrimaryKey)
		if err != nil {
			return fmt.Errorf("querying rows: %s", err.Error())
		}

		for _, row := range rows {
			err = r.queryBuilder.ResolveRelationships(context.Background(), *row)
			if err != nil {
				return fmt.Errorf("resolving relationships: %s", err.Error())
			}
			err = r.transformer.Transform(table, node, *row)
			if err != nil {
				return fmt.Errorf("transforming: %s", err.Error())
			}
		}

		err = r.Syncer.TryFullReplication(rows)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *LogicalReplicator) initPublication() error {
	result := r.conn.Exec(context.Background(), fmt.Sprintf("DROP PUBLICATION IF EXISTS %s;", r.PublicationName))
	_, err := result.ReadAll()
	if err != nil {
		return err
	}

	tableStr := strings.Builder{}
	idx := 0
	for table, node := range r.Schema.Nodes {
		if node.Sync == SYNC_NONE || node.Sync == SYNC_INIT {
			continue
		}
		if idx > 0 {
			tableStr.WriteString(", ")
		}
		tableStr.WriteString(table)
		idx++
	}

	if idx <= 0 {
		return errors.New("no publishable tables")
	}

	createPubQuery := fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s;", r.PublicationName, tableStr.String())
	log.Println(createPubQuery)
	result = r.conn.Exec(context.Background(), createPubQuery)
	_, err = result.ReadAll()
	if err != nil {
		return err
	}
	log.Println("created publication:", r.PublicationName)
	return nil
}

func (r *LogicalReplicator) initReplicationSlot() error {
	slotInfo := r.getSlotInfo()

	if slotInfo == nil {
		result, err := pglogrepl.CreateReplicationSlot(
			context.Background(),
			r.conn,
			r.SlotName,
			r.OutputPlugin,
			pglogrepl.CreateReplicationSlotOptions{
				Temporary: false,
			})
		if err != nil {
			return err
		}
		log.Println("Created replication slot:", r.SlotName)
		r.slotCreationInfo = &result
	}

	sysident, err := pglogrepl.IdentifySystem(context.Background(), r.conn)
	if err != nil {
		return err
	}
	log.Println("SystemID:", sysident.SystemID, "Timeline:", sysident.Timeline, "XLogPos:", sysident.XLogPos, "DBName:", sysident.DBName)

	if r.slotCreationInfo != nil {
		r.state.defaultStartingPos, err = pglogrepl.ParseLSN(r.slotCreationInfo.ConsistentPoint)
		if err != nil {
			return err
		}
	} else if slotInfo == nil || !slotInfo.hasValidFlushedLsn {
		r.state.defaultStartingPos = sysident.XLogPos
	} else {
		r.state.defaultStartingPos = sysident.XLogPos
		if slotInfo.latestFlushedLsn < sysident.XLogPos {
			r.state.defaultStartingPos = slotInfo.latestFlushedLsn
			log.Println("Found previous flushed LSN:", slotInfo.latestFlushedLsn)
		}
	}

	return nil
}

func (r *LogicalReplicator) getSlotInfo() *replicationSlot {
	dbConfig, err := pgconn.ParseConfig(r.ConnectionString)
	if err != nil {
		log.Fatalln("Parsing connection string failed:", err)
	}

	query := fmt.Sprintf("SELECT active, restart_lsn, confirmed_flush_lsn, catalog_xmin FROM pg_replication_slots WHERE "+
		"slot_name = '%s' AND database = '%s' AND plugin = '%s'",
		r.SlotName, dbConfig.Database, r.OutputPlugin)
	result, err := r.conn.Exec(context.Background(), query).ReadAll()
	if err != nil {
		log.Fatalln("Failed querying slot info:", err)
	}

	row := result[0].Rows[0]
	fieldDesc := result[0].FieldDescriptions
	if len(row) == 4 {
		active, _ := r.queryBuilder.decode(
			row[0], fieldDesc[0].DataTypeOID, fieldDesc[0].Format)
		restartLsn, _ := r.queryBuilder.decode(
			row[1], fieldDesc[1].DataTypeOID, fieldDesc[0].Format)
		latestFlushedLsn, _ := r.queryBuilder.decode(
			row[2], fieldDesc[2].DataTypeOID, fieldDesc[0].Format)
		catalogXmin, _ := r.queryBuilder.decode(
			row[3], fieldDesc[3].DataTypeOID, fieldDesc[0].Format)

		parsedRestartLsn, _ := pglogrepl.ParseLSN(restartLsn.(string))
		parsedLatestFlushedLsn, _ := pglogrepl.ParseLSN(latestFlushedLsn.(string))

		// TODO: not sure about this
		lsnFromFile, err := r.readWALPosition()
		if err == nil {
			log.Println("Reading last flushed WAL position from file:", lsnFromFile.String())
			parsedLatestFlushedLsn = lsnFromFile
		}

		slotInfo := &replicationSlot{
			active:           active.(bool),
			restartLsn:       parsedRestartLsn,
			latestFlushedLsn: parsedLatestFlushedLsn,
			catalogXmin:      catalogXmin.(uint32),
		}

		slotInfo.hasValidFlushedLsn = true

		return slotInfo
	}

	return nil
}

func (r *LogicalReplicator) readWALPosition() (pglogrepl.LSN, error) {
	data, err := os.ReadFile("progress")
	if err != nil {
		return 0, err
	}
	if len(data) != 8 {
		return 0, errors.New("malformed WAL position data")
	}
	return pglogrepl.LSN(binary.LittleEndian.Uint64(data)), nil
}

func (r *LogicalReplicator) writeWALPosition(lsn pglogrepl.LSN) error {
	bytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(bytes, uint64(lsn))
	return os.WriteFile("progress", bytes, 0644)
}

func (r *LogicalReplicator) processMessage(xld pglogrepl.XLogData) (bool, error) {
	logicalMsg, err := pglogrepl.ParseV2(xld.WALData, r.state.inStream)
	if err != nil {
		log.Fatalf("Parse logical replication message: %s", err)
	}
	log.Printf("Receive a logical replication message: %s", logicalMsg.Type())

	switch logicalMsg := logicalMsg.(type) {
	case *pglogrepl.RelationMessageV2:
		r.state.relations[logicalMsg.RelationID] = logicalMsg
	case *pglogrepl.BeginMessage:
		if r.state.lastWrittenLSN > logicalMsg.FinalLSN {
			log.Printf("Received stale message, ignoring. Last written LSN: %s Message LSN: %s",
				r.state.lastWrittenLSN, logicalMsg.FinalLSN)
			r.state.processMessages = false
			return false, nil
		}

		r.state.processMessages = true
		r.state.currentTransactionLSN = logicalMsg.FinalLSN

		err := r.Syncer.OnBegin(logicalMsg.Xid)
		if err != nil {
			return false, err
		}
	case *pglogrepl.CommitMessage:
		err := r.Syncer.OnCommit()
		if err != nil {
			return false, err
		}
		r.state.processMessages = false
		return true, nil
	case *pglogrepl.InsertMessageV2:
		return r.handleInsert(logicalMsg)
	case *pglogrepl.UpdateMessageV2:
		// TODO: handle update
		log.Println("TODO: handle update message")
	case *pglogrepl.DeleteMessageV2:
		return r.handleDelete(logicalMsg)
	case *pglogrepl.TruncateMessageV2:
		log.Printf("truncate for xid %d\n", logicalMsg.Xid)
	case *pglogrepl.TypeMessageV2:
		log.Printf("typeMessage for xid %d\n", logicalMsg.Xid)
	case *pglogrepl.OriginMessage:
		log.Printf("originMessage for xid %s\n", logicalMsg.Name)
	case *pglogrepl.LogicalDecodingMessageV2:
		log.Printf("Logical decoding message: %q, %q, %d", logicalMsg.Prefix, logicalMsg.Content, logicalMsg.Xid)
	case *pglogrepl.StreamStartMessageV2:
		r.state.inStream = true
		log.Printf("Stream start message: xid %d, first segment? %d", logicalMsg.Xid, logicalMsg.FirstSegment)
	case *pglogrepl.StreamStopMessageV2:
		r.state.inStream = false
		log.Printf("Stream stop message")
	case *pglogrepl.StreamCommitMessageV2:
		log.Printf("Stream commit message: xid %d", logicalMsg.Xid)
	case *pglogrepl.StreamAbortMessageV2:
		log.Printf("Stream abort message: xid %d", logicalMsg.Xid)
	default:
		log.Printf("Unknown message type in pgoutput stream: %T", logicalMsg)
	}

	return false, nil
}

func (r *LogicalReplicator) handleInsert(logicalMsg *pglogrepl.InsertMessageV2) (bool, error) {
	if !r.state.processMessages {
		log.Printf("Received stale message, ignoring. Last written LSN: %s Message LSN: %s",
			r.state.lastWrittenLSN, r.state.lastReceivedLSN)
		return false, nil
	}

	rel, ok := r.state.relations[logicalMsg.RelationID]
	if !ok {
		log.Fatalf("unknown relation ID %d", logicalMsg.RelationID)
	}

	data := DmlData{
		TableName: rel.RelationName,
		Fields:    r.collectFields(logicalMsg.Tuple.Columns, rel),
	}

	err := r.queryBuilder.ResolveRelationships(context.Background(), data)
	if err != nil {
		return false, err
	}
	err = r.transformer.Transform(rel.RelationName, r.Schema.Nodes[data.TableName], data)
	if err != nil {
		return false, err
	}

	err = r.Syncer.OnInsert(data)
	return false, err
}

func (r *LogicalReplicator) handleDelete(logicalMsg *pglogrepl.DeleteMessageV2) (bool, error) {
	rel, ok := r.state.relations[logicalMsg.RelationID]
	if !ok {
		log.Fatalf("unknown relation ID %d", logicalMsg.RelationID)
	}

	data := DmlData{
		TableName: rel.RelationName,
		Fields:    r.collectFields(logicalMsg.OldTuple.Columns, rel),
	}

	err := r.Syncer.OnDelete(data)
	return false, err
}

func (r *LogicalReplicator) collectFields(
	columns []*pglogrepl.TupleDataColumn,
	relation *pglogrepl.RelationMessageV2,
) map[string]Field {
	fields := map[string]Field{}
	for idx, column := range columns {
		columnName := relation.Columns[idx].Name

		table := r.Schema.Nodes[relation.RelationName]
		max := len(table.Columns)
		foundIdx := sort.Search(max, func(i int) bool {
			return table.Columns[i] == columnName
		})

		// Check if this column is a key, if not then don't skip it as it will be important for later use
		isKey := relation.Columns[idx].Flags == 1
		if foundIdx == max && !isKey {
			continue
		}

		fullyQualifiedColumnName := fmt.Sprintf("%s.%s", relation.RelationName, columnName)
		switch column.DataType {
		case 'n': // null
			fields[fullyQualifiedColumnName] = Field{}
		case 'u': // unchanged toast
			// This TOAST value was not changed. TOAST values are not stored in the tuple,
			// and logical replication doesn't want to spend a disk read to fetch its value for you.
		case 't': //text
			decoded, err := r.queryBuilder.decode(column.Data, relation.Columns[idx].DataType, pgtype.TextFormatCode)
			if err != nil {
				log.Fatalln("error decoding column data:", err)
			}
			fields[fullyQualifiedColumnName] = Field{
				Content: decoded,
				IsKey:   isKey,
			}
		}
	}

	return fields
}