package main

import (
	"log"
	"os"
	"strconv"
	"time"

	"github.com/athariqk/pgcdc/logrepl"
	"github.com/athariqk/pgcdc/publishers"
	"github.com/joho/godotenv"
)

// Source - https://stackoverflow.com/a/40326580
// Posted by janos, modified by community. See post 'Timeline' for change history
// Retrieved and modified 2026-01-06, License - CC BY-SA 3.0
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	log.Default().Printf("Env variable %s using fallback value: %s", key, fallback)
	return fallback
}

func main() {
	env := getEnv("PGCDC_ENV", "development")

	_mode, err := strconv.Atoi(getEnv("PGCDC_MODE", "0"))
	if err != nil {
		_mode = 0
	}
	mode := logrepl.ReplicationMode(_mode)

	godotenv.Load(".env." + env + ".local")
	if env != "test" {
		godotenv.Load(".env.local")
	}
	godotenv.Load(".env." + env)
	godotenv.Load() // The Original .env

	log.Println("Environment:", env)

	outputPlugin := getEnv("OUTPUT_PLUGIN", "pgoutput")
	connectionString := getEnv("PGSQL_CONNECTION_STRING", "")
	if connectionString == "" {
		panic("PGSQL connection string is required but empty")
	}
	pubName := getEnv("PGSQL_PUB_NAME", "")
	if pubName == "" {
		panic("PGSQL publication name is required but empty")
	}
	slotName := getEnv("PGSQL_REPL_SLOT_NAME", "")
	if slotName == "" {
		panic("PGSQL replication slot name is required but empty")
	}
	standbyMessageTimeout, err := strconv.Atoi(getEnv("PGSQL_STANDBY_MESSAGE_TIMEOUT", "10"))
	if err != nil {
		standbyMessageTimeout = 10
	}

	pubs := []logrepl.Publisher{
		publishers.NewNsqPublisher(
			getEnv("NSQD_INSTANCE_ADDRESS", "localhost"),
			getEnv("NSQD_INSTANCE_PORT", "4150"),
			getEnv("NSQ_TOPIC", "replication")),
	}

	replicator := logrepl.LogicalReplicator{
		OutputPlugin:          outputPlugin,
		ConnectionString:      connectionString,
		PublicationName:       pubName,
		SlotName:              slotName,
		StandbyMessageTimeout: time.Second * time.Duration(standbyMessageTimeout),
		Publishers:            pubs,
		Schema:                logrepl.NewSchema("schema.yaml"),
		Mode:                  mode,
	}

	replicator.Run()
}
