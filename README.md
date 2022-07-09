# protoscan

[![Build Status](https://cloud.drone.io/api/badges/protoscan/protoscan/status.svg)](https://cloud.drone.io/protoscan/protoscan)
[![Go Reference](https://pkg.go.dev/badge/github.com/protoscan/protoscan.svg)](https://pkg.go.dev/github.com/protoscan/protoscan)

Protocol scanner for Go.  
Source files are distributed under the BSD-style license.

## Benchmark

```sh
$ go test -run ^NOTHING -bench BenchmarkScanRune\$
goos: linux
goarch: amd64
pkg: github.com/protoscan/protoscan
cpu: 11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz
BenchmarkScanRune-8   	 1894521	       618.2 ns/op	      40 B/op	       2 allocs/op
PASS
ok  	github.com/protoscan/protoscan	1.815s
```
