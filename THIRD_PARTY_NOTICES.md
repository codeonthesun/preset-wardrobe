# Third-party software

PresetWardrobe.exe is built with these libraries. Their licenses apply to their code.

| Library | Used for | License |
|---|---|---|
| [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3) | Reading and writing the game profile | MIT |
| [SQLite](https://sqlite.org) (embedded by go-sqlite3) | The database engine | Public domain |
| [tetratelabs/wazero](https://github.com/tetratelabs/wazero) | Runs the embedded SQLite | Apache-2.0 |
| [ncruces/julianday](https://github.com/ncruces/julianday) | Dependency of go-sqlite3 | MIT |
| [jchv/go-webview2](https://github.com/jchv/go-webview2) | The program's window | MIT |
| [jchv/go-winloader](https://github.com/jchv/go-winloader) | Dependency of go-webview2 | MIT |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | Windows system calls | BSD-3-Clause |

Release builds also use [tc-hib/go-winres](https://github.com/tc-hib/go-winres) (MIT) to add the
icon and version information; it isn't part of the program.
