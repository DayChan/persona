import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent

# A fake docker. $TEST_CONTAINERS lists the containers there as name=user (the POSTGRES_USER each was made with, what
# `inspect … .Config.Env` answers), $TEST_VOLUMES the volumes. `inspect` answers a network mode ($TEST_NETWORK) for
# anything else, and `exec … pg_isready` answers like the real one for a Postgres listening on $TEST_LISTEN_PORT inside
# the container: ready when the port it asks (PGPORT, else 5432) is that one. Every call is logged to $TEST_LOG.
FAKE_DOCKER = r'''#!/bin/bash
printf '%s\n' "$*" >> "$TEST_LOG"
user_of() { for e in $TEST_CONTAINERS; do [ "${e%%=*}" = "$1" ] && { echo "${e#*=}"; return 0; }; done; return 1; }
case "$1" in
  ps) for e in $TEST_CONTAINERS; do echo "${e%%=*}"; done ;;
  start|run) ;;
  volume)
    case "$2" in
      inspect) for v in $TEST_VOLUMES; do [ "$v" = "$3" ] && exit 0; done; exit 1 ;;
    esac ;;
  inspect)
    case "$3" in
      *Config.Env*) u=$(user_of "$4") || exit 1; echo "POSTGRES_USER=$u"; echo "POSTGRES_PASSWORD=pw" ;;
      *) echo "$TEST_NETWORK" ;;
    esac ;;
  exec)
    shift; port=5432
    while [ "$1" = -e ]; do case "$2" in PGPORT=*) port=${2#PGPORT=} ;; esac; shift 2; done
    shift # the container
    case "$1" in
      pg_isready)
        if [ "$port" = "$TEST_LISTEN_PORT" ]; then echo "/var/run/postgresql:$port - accepting connections"
        else echo "/var/run/postgresql:$port - no response"; exit 2; fi ;;
      psql) echo "$TEST_TEST_DB" ;;
    esac ;;
esac
'''
# The host's pg_isready, for a database_url no container serves: logged, and it answers.
FAKE_PG_ISREADY = '#!/bin/sh\nprintf "host pg_isready %s\\n" "$*" >> "$TEST_LOG"\n'

LEGACY_URL = "postgres://jarvis:pw@127.0.0.1:18661/jarvis?sslmode=disable"
NEW_URL = "postgres://persona:pw@127.0.0.1:18661/persona?sslmode=disable"


class DbShTestCase(unittest.TestCase):
    """db.sh with an existing container, against the fake docker above (and a sleep that returns at once)."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="persona-db-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state, self.tools = self.root / "state", self.root / "tools"
        self.state.mkdir()
        self.tools.mkdir()
        self.log = self.root / "docker.log"
        for name, text in (("docker", FAKE_DOCKER), ("sleep", "#!/bin/sh\n"), ("pg_isready", FAKE_PG_ISREADY)):
            (self.tools / name).write_text(text)
            (self.tools / name).chmod(0o755)

    def run_db(self, network, listen_port, url_port):
        return self.run_with(
            {"database_url": "postgres://persona:pw@127.0.0.1:%d/persona?sslmode=disable" % url_port},
            containers="persona-postgres=persona", network=network, listen_port=listen_port)

    def run_with(self, config, containers="", volumes="", network="bridge", listen_port=5432, test_db="1"):
        """db.sh with hub.json holding config (None: no hub.json) and the containers / volumes given."""
        if config is not None:
            (self.state / "hub.json").write_text(json.dumps(config))
        env = {"PATH": str(self.tools) + os.pathsep + os.defpath, "HOME": str(self.root),
               "_PERSONA_STATE_DIR": str(self.state), "TEST_LOG": str(self.log),
               "TEST_NETWORK": network, "TEST_LISTEN_PORT": str(listen_port), "TEST_CONTAINERS": containers,
               "TEST_VOLUMES": volumes, "TEST_TEST_DB": test_db}
        return subprocess.run(["bash", str(SCRIPTS / "db.sh")], env=env, capture_output=True, text=True, timeout=30)

    def calls(self):
        return self.log.read_text() if self.log.exists() else ""

    def assert_nothing_made(self):
        calls = self.calls()
        self.assertNotIn("\nrun ", "\n" + calls)
        self.assertNotIn("volume create", calls)
        self.assertNotIn("createdb -U persona persona_test", calls)

    def test_a_container_on_the_host_network_is_asked_on_the_port_the_hub_connects_to(self):
        # --network host with postgres -c port=…: inside the container Postgres listens where database_url says
        result = self.run_db("host", 25432, 25432)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("postgres ready", result.stdout)
        self.assertIn("exec -e PGPORT=25432 persona-postgres pg_isready", self.log.read_text())

    def test_the_container_db_sh_creates_is_asked_on_5432_not_the_published_port(self):
        # -p 127.0.0.1:$PORT:5432: database_url has the published port, Postgres in the container listens on 5432
        result = self.run_db("bridge", 5432, 15432)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("exec -e PGPORT=5432 persona-postgres pg_isready", self.log.read_text())
        self.assertIn("exec -e PGPORT=5432 persona-postgres psql", self.log.read_text())

    def test_a_database_that_never_answers_stops_the_deploy_with_the_reason(self):
        result = self.run_db("host", 5432, 25432)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not ready on port 25432", result.stderr)
        self.assertIn("no response", result.stderr)
        self.assertNotIn("psql", self.log.read_text())

    # #6149: a hub set up before the rename keeps its database.

    def test_a_hub_set_up_before_the_rename_keeps_its_jarvis_postgres(self):
        before = json.dumps({"database_url": LEGACY_URL, "token": "t"})
        (self.state / "hub.json").write_text(before)
        result = self.run_with(None, containers="jarvis-postgres=jarvis", volumes="jarvis-pgdata", test_db="")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("(jarvis-postgres)", result.stdout)
        self.assert_nothing_made()
        calls = self.calls()
        self.assertIn("start jarvis-postgres", calls)
        self.assertIn("exec -e PGPORT=5432 jarvis-postgres pg_isready -U jarvis -d jarvis", calls)
        # the throwaway test database is the configured one's, made as its user
        self.assertIn("exec -e PGPORT=5432 jarvis-postgres createdb -U jarvis jarvis_test", calls)
        self.assertEqual((self.state / "hub.json").read_text(), before)

    def test_with_both_containers_the_one_that_serves_database_url_is_used(self):
        # what a deploy of the renamed db.sh left on such a hub: an empty persona-postgres next to the real one
        before = json.dumps({"database_url": LEGACY_URL})
        (self.state / "hub.json").write_text(before)
        result = self.run_with(None, containers="persona-postgres=persona jarvis-postgres=jarvis",
                               volumes="persona-pgdata jarvis-pgdata")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("persona-postgres exists but hub.json's database_url is not its database", result.stderr)
        self.assert_nothing_made()
        calls = self.calls()
        self.assertIn("start jarvis-postgres", calls)
        self.assertNotIn("start persona-postgres", calls)
        self.assertNotIn("exec -e PGPORT=5432 persona-postgres", calls)
        self.assertEqual((self.state / "hub.json").read_text(), before)

    def test_a_database_url_no_container_serves_is_used_as_it_is(self):
        # database_url's user is persona but only the old container is there: neither is touched nor anything made
        before = json.dumps({"database_url": NEW_URL})
        (self.state / "hub.json").write_text(before)
        result = self.run_with(None, containers="jarvis-postgres=jarvis", volumes="jarvis-pgdata")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("nothing made or changed", result.stdout)
        self.assert_nothing_made()
        calls = self.calls()
        self.assertNotIn("start ", calls)
        self.assertIn("host pg_isready -q -d " + NEW_URL, calls)
        self.assertEqual((self.state / "hub.json").read_text(), before)

    def test_a_database_url_without_any_container_is_never_replaced(self):
        before = json.dumps({"database_url": "postgres://me:pw@db.example.com:5432/hub?sslmode=disable"})
        (self.state / "hub.json").write_text(before)
        result = self.run_with(None)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assert_nothing_made()
        self.assertEqual((self.state / "hub.json").read_text(), before)

    def test_an_old_container_without_database_url_stops_and_makes_nothing(self):
        result = self.run_with({"token": "t"}, containers="jarvis-postgres=jarvis", volumes="jarvis-pgdata")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("container jarvis-postgres exists but hub.json has no database_url", result.stderr)
        self.assert_nothing_made()
        self.assertNotIn("database_url", (self.state / "hub.json").read_text())

    def test_an_old_volume_without_its_container_stops_and_makes_nothing(self):
        result = self.run_with({}, volumes="jarvis-pgdata")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("volume jarvis-pgdata exists", result.stderr)
        self.assert_nothing_made()
        self.assertEqual(json.loads((self.state / "hub.json").read_text()), {})

    def test_a_new_hub_gets_a_persona_postgres_and_its_database_url(self):
        result = self.run_with(None)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.calls()
        self.assertIn("volume create persona-pgdata", calls)
        self.assertIn("run -d --name persona-postgres", calls)
        self.assertIn("-v persona-pgdata:/var/lib/postgresql", calls)
        self.assertIn("POSTGRES_DB=persona postgres:18-alpine postgres -c wal_compression=lz4 -c checkpoint_timeout=15min\n", calls)
        url = json.loads((self.state / "hub.json").read_text())["database_url"]
        self.assertRegex(url, r"^postgres://persona:[0-9a-f]{48}@127\.0\.0\.1:18661/persona\?sslmode=disable$")
        self.assertEqual((self.state / "hub.json").stat().st_mode & 0o777, 0o600)
        self.assertIn("exec -e PGPORT=5432 persona-postgres pg_isready -U persona -d persona", calls)



if __name__ == "__main__":
    unittest.main()
