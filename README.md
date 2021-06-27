# protoscan

[![Build Status](https://cloud.drone.io/api/badges/protoscan/protoscan/status.svg)](https://cloud.drone.io/protoscan/protoscan)
[![Go Reference](https://pkg.go.dev/badge/github.com/protoscan/protoscan.svg)](https://pkg.go.dev/github.com/protoscan/protoscan)

Protocol scanner for Go.  
Source files are distributed under the BSD-style license
found in the [LICENSE](./LICENSE) file.

## Install

    go get github.com/protoscan/protoscan@v0.3.0

## Benchmark

```
go test -bench=. ./...
goarch: amd64
pkg: github.com/protoscan/protoscan
cpu: 11th Gen Intel(R) Core(TM) i5-1135G7 @ 2.40GHz
BenchmarkScanRune
BenchmarkScanRune-8   	 1206433	       973.3 ns/op	     152 B/op	      16 allocs/op
```
