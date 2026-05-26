# AFL++ Idle Watchdog

`cmd/afl-idle-watchdog` monitors AFL++ `fuzzer_stats` metrics and stops fuzzing when no new path is observed for the configured idle window.

Default CI/CD environment:

```bash
export AFL_STATS_DIR="${AFL_STATS_DIR:-fuzz/out}"
export AFL_IDLE_TIMEOUT="${AFL_IDLE_TIMEOUT:-2h}"
export AFL_WATCHDOG_POLL_INTERVAL="${AFL_WATCHDOG_POLL_INTERVAL:-60s}"
export AFL_WATCHDOG_TERMINATE_TIMEOUT="${AFL_WATCHDOG_TERMINATE_TIMEOUT:-30s}"
export AFL_WATCHDOG_EXIT_CODE="${AFL_WATCHDOG_EXIT_CODE:-124}"
```

Run AFL++ through the wrapper:

```bash
scripts/afl-ci-watchdog.sh afl-fuzz -i fuzz/in -o "$AFL_STATS_DIR" -- ./target @@
```

Monitor an already running AFL++ output directory:

```bash
AFL_STATS_DIR=fuzz/out AFL_IDLE_TIMEOUT=2h scripts/afl-ci-watchdog.sh
```

The watchdog reads every `fuzzer_stats` file under `AFL_STATS_DIR`, including multi-fuzzer layouts such as `fuzz/out/default/fuzzer_stats`. It uses `last_find` or `last_path` as the primary AFL++ “new path” timestamp, and also treats increases in path counters such as `paths_total`, `paths_found`, `paths_imported`, and `corpus_count` as progress.

On idle timeout it sends `SIGTERM`, waits `AFL_WATCHDOG_TERMINATE_TIMEOUT`, then sends `SIGKILL` to the fuzzing process group when it launched AFL++, or to `fuzzer_pid` values from `fuzzer_stats` when monitoring an external run.
