#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "[1/3] Full test suite"
go test -count=1 ./...

echo
echo "[2/3] Policy benchmark regression check"
go test -run '^$' -bench 'BenchmarkResolveGoldenScenarios' -benchmem -benchtime=100x ./internal/runtime/policy

echo
echo "[3/3] End-to-end benchmark regression check"
go test -run '^$' -bench 'BenchmarkRunParmesanGoldenScenarios|BenchmarkRunParmesanFullGoldenCorpus' -benchmem -benchtime=50x ./internal/parity
