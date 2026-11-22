#!/bin/sh
# Builds the C++ helper of the differential LevelDB test
# (testdata/leveldb-cxx/leveldb_cxx.cc) against the C++ LevelDB library.
#
# usage: build-leveldb-cxx.sh OUT
#
# The library is taken from LEVELDB_PREFIX and SNAPPY_PREFIX, or from
# Homebrew (brew install leveldb snappy). The test is run with
#   PARITYDESK_LEVELDB_CXX=OUT go test -run TestDifferential ./internal/store/kv/leveldb/
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: $0 OUT" >&2
	exit 2
fi
out=$1

if [ -z "${LEVELDB_PREFIX:-}" ] || [ -z "${SNAPPY_PREFIX:-}" ]; then
	if ! command -v brew >/dev/null 2>&1; then
		echo "build-leveldb-cxx: set LEVELDB_PREFIX and SNAPPY_PREFIX, or install Homebrew's leveldb and snappy" >&2
		exit 1
	fi
fi
leveldb_prefix=${LEVELDB_PREFIX:-$(brew --prefix leveldb)}
snappy_prefix=${SNAPPY_PREFIX:-$(brew --prefix snappy)}
for f in "$leveldb_prefix/include/leveldb/db.h" "$snappy_prefix/include/snappy.h"; do
	if [ ! -f "$f" ]; then
		echo "build-leveldb-cxx: $f not found" >&2
		exit 1
	fi
done

root=$(cd "$(dirname "$0")/../.." && pwd -P)
src="$root/testdata/leveldb-cxx/leveldb_cxx.cc"

c++ -std=c++17 -O2 -Wall -Wextra \
	-I"$leveldb_prefix/include" \
	-o "$out" "$src" \
	-L"$leveldb_prefix/lib" -lleveldb \
	-L"$snappy_prefix/lib" -lsnappy \
	-Wl,-rpath,"$leveldb_prefix/lib" -Wl,-rpath,"$snappy_prefix/lib"
echo "built $out"
