# Integrated rtlamr-collect

This command starts from upstream
[`bemasher/rtlamr-collect`](https://github.com/bemasher/rtlamr-collect)
`v1.0.3` (commit `7951486ab74b6d86f8bbe094a3531dab46728884`) so the
decoder and its production collector can be built from this repository.

Build it with:

```sh
go install ./cmd/rtlamr-collect
```

All upstream environment variables retain their original behavior. This fork
adds one opt-in setting:

```text
COLLECT_INFLUXDB_UNCHANGED_INTERVAL=60s
```

When the setting is a positive Go duration, R900 and R900BCD points are sent
immediately when any Influx-visible field changes. While all five fields remain
unchanged, the collector sends only the first genuine reception at or after
the configured interval. The default is disabled (`0` or unset), preserving
upstream behavior. Process restart and non-advancing timestamps fail open by
emitting the next point.

IDM and NetIDM are deliberately excluded. Their cumulative and historical
interval points continue through the upstream BoltDB-backed de-duplication path
unchanged.
