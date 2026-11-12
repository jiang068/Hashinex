# Third-party notices

Hashinex uses the Go standard library and the Go modules listed in `go.mod` and `go.sum`. The original license and notice files for the linked modules and the Go runtime can be collected with `scripts/collect_licenses.py`.

| Dependency | Purpose | License |
| --- | --- | --- |
| [coder/websocket](https://github.com/coder/websocket) | WebSocket transport | ISC |
| [go-yaml v3](https://github.com/go-yaml/yaml) | YAML configuration | MIT and Apache-2.0, as described in the upstream LICENSE and NOTICE |
| [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) | SQLite without CGO | BSD-3-Clause for the Go module, with SQLite upstream in the public domain |
| [Go](https://go.dev/) | Runtime and standard library | BSD-3-Clause, plus upstream notices for bundled code |

Hashinex implements its own adapters and identity logic. The [README acknowledgements](README.md#参考项目与致谢) list the projects that informed the implementation. The referenced napcat-plugin-gscore-adapter is licensed under MIT, Copyright (c) 2026 MortalCat. External installations retain their upstream licenses.
