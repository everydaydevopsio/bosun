# Lessons

## 2026-09-24 — Build prerequisites must be explicit

`make test` invoked protobuf generation before it ensured the Python virtualenv
containing `grpcio-tools` existed. The Go migration will make generated-code
dependencies an explicit Makefile prerequisite so a clean checkout can run the
documented test command.
