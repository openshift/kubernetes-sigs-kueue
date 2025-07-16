[![Tests on Linux, MacOS and Windows](https://github.com/bep/mclib/workflows/Test/badge.svg)](https://github.com/bep/mclib/actions?query=workflow:Test)
[![Go Report Card](https://goreportcard.com/badge/github.com/bep/mclib)](https://goreportcard.com/report/github.com/bep/mclib)
[![GoDoc](https://godoc.org/github.com/bep/mclib?status.svg)](https://godoc.org/github.com/bep/mclib)


This is a simple library to make it possible to run [Mkcert's](https://github.com/FiloSottile/mkcert)  `main` method.

The script that updates the `internal` package does no logic changes to the source, it simply

1. Renames the `main` package to `internal`.
1. Renames the `main` func to `RunMain`
1. Replaces any `log.Fatal` with `panic` to allow us to handle the errors.
1. Exports getCAROOT().

For more advanced library usage, see [this issue](https://github.com/FiloSottile/mkcert/issues/45).

The `mkcert` source code is stored in a Git submodule to a tagged version, so to generate a new version, you need to clone this repo with `--recursive`, and then run:

```bash
go generate ./gen
```

We use [semverpair](https://github.com/bep/semverpair) versioning.