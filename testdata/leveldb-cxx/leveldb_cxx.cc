// leveldb-cxx is the C++ side of the differential LevelDB test in
// internal/store/kv/leveldb/differential_test.go. It links the C++
// LevelDB library (Homebrew's leveldb and snappy on CI) and is built by
// scripts/ci/build-leveldb-cxx.sh. Only the source is kept in the
// repository.
//
// usage:
//   leveldb-cxx write DIR none|snappy SEED
//       Creates a new database in DIR with a fixed workload: thousands of
//       keys of three shapes (plain, Local Storage style with NUL and 0x01
//       bytes, and binary), empty, small, compressible, random and
//       multi-block values, deletions, overwrites, a full compaction, then
//       a batch and single writes that stay in the log. Small write
//       buffers and file sizes give many table files. Prints the expected
//       content: "P <hex key> <hex value>" for every live pair in key
//       order, then "D <hex key>" for every deleted key. The last writes
//       overlap background compactions, so the database is usually left
//       with a journal whose first batches are numbered below the
//       manifest's last sequence (internal/store/kv/leveldb/journal.go);
//       this depends on timing.
//   leveldb-cxx dump DIR
//       Opens the existing database in DIR and prints "P <hex key> <hex
//       value>" for every pair in key order.
//
// Exit status 0 on success, 1 on a LevelDB error, 2 on a usage error.

#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <map>
#include <set>
#include <string>
#include <vector>

#include "leveldb/db.h"
#include "leveldb/iterator.h"
#include "leveldb/options.h"
#include "leveldb/write_batch.h"

namespace {

const int kKeys = 3000;

// Rng is xorshift64*, so every build produces the same workload.
class Rng {
 public:
  explicit Rng(uint64_t seed) : s_(seed != 0 ? seed : 0x9e3779b97f4a7c15ULL) {}
  uint64_t Next() {
    s_ ^= s_ >> 12;
    s_ ^= s_ << 25;
    s_ ^= s_ >> 27;
    return s_ * 2685821657736338717ULL;
  }
  uint64_t Below(uint64_t n) { return Next() % n; }

 private:
  uint64_t s_;
};

std::string Hex(const std::string& s) {
  static const char kDigits[] = "0123456789abcdef";
  std::string out;
  out.reserve(s.size() * 2);
  for (unsigned char c : s) {
    out.push_back(kDigits[c >> 4]);
    out.push_back(kDigits[c & 15]);
  }
  return out;
}

// Key returns the i-th key in one of three shapes.
std::string Key(int i) {
  char buf[32];
  switch (i % 3) {
    case 0:
      std::snprintf(buf, sizeof buf, "k%08d", i);
      return std::string(buf);
    case 1: {
      std::string k = "_https://example.com";
      k.push_back('\0');
      k.push_back('\x01');
      std::snprintf(buf, sizeof buf, "item-%d", i);
      return k + buf;
    }
    default: {
      std::string k = "\xff";
      k.push_back('\0');
      k.push_back('b');
      for (int shift = 24; shift >= 0; shift -= 8) {
        k.push_back(static_cast<char>((static_cast<uint32_t>(i) >> shift) & 0xff));
      }
      return k;
    }
  }
}

std::string Repeat(const std::string& unit, size_t n) {
  std::string out;
  out.reserve(n);
  while (out.size() < n) {
    out += unit;
  }
  out.resize(n);
  return out;
}

// Value returns a value for key i; its shape is picked by the generator.
std::string Value(Rng& r, int i, int round) {
  char unit[48];
  std::snprintf(unit, sizeof unit, "value-%d-round-%d;", i, round);
  if (i % 100 == 42) {
    return Repeat(unit, 20000 + r.Below(40000));  // spans many blocks
  }
  switch (r.Below(10)) {
    case 0:
      return std::string();
    case 1:
    case 2:
    case 3:
    case 4:
    case 5:
      return Repeat(unit, 200 + r.Below(2800));  // compressible
    case 6:
    case 7: {
      std::string v(r.Below(1500), '\0');
      for (auto& c : v) {
        c = static_cast<char>(r.Next() & 0xff);  // incompressible
      }
      return v;
    }
    default:
      return Repeat(unit, 1 + r.Below(40));
  }
}

int Fail(const char* what, const leveldb::Status& st) {
  std::fprintf(stderr, "leveldb-cxx: %s: %s\n", what, st.ToString().c_str());
  return 1;
}

class Workload {
 public:
  explicit Workload(leveldb::DB* db) : db_(db) {}

  leveldb::Status Put(const std::string& k, const std::string& v) {
    leveldb::Status st = db_->Put(leveldb::WriteOptions(), k, v);
    if (st.ok()) {
      model_[k] = v;
      deleted_.erase(k);
    }
    return st;
  }

  leveldb::Status Delete(const std::string& k) {
    leveldb::Status st = db_->Delete(leveldb::WriteOptions(), k);
    if (st.ok()) {
      model_.erase(k);
      deleted_.insert(k);
    }
    return st;
  }

  // BatchPut and BatchDelete stage operations for Commit.
  void BatchPut(const std::string& k, const std::string& v) {
    batch_.Put(k, v);
    staged_.push_back(Op{k, v, false});
  }
  void BatchDelete(const std::string& k) {
    batch_.Delete(k);
    staged_.push_back(Op{k, std::string(), true});
  }

  leveldb::Status Commit() {
    leveldb::WriteOptions wo;
    wo.sync = true;
    leveldb::Status st = db_->Write(wo, &batch_);
    if (st.ok()) {
      for (const Op& op : staged_) {
        if (op.del) {
          model_.erase(op.key);
          deleted_.insert(op.key);
        } else {
          model_[op.key] = op.value;
          deleted_.erase(op.key);
        }
      }
    }
    batch_.Clear();
    staged_.clear();
    return st;
  }

  void Print() const {
    for (const auto& kv : model_) {
      std::printf("P %s %s\n", Hex(kv.first).c_str(), Hex(kv.second).c_str());
    }
    for (const auto& k : deleted_) {
      std::printf("D %s\n", Hex(k).c_str());
    }
  }

 private:
  struct Op {
    std::string key;
    std::string value;
    bool del;
  };
  leveldb::DB* db_;
  leveldb::WriteBatch batch_;
  std::vector<Op> staged_;
  std::map<std::string, std::string> model_;
  std::set<std::string> deleted_;
};

int Write(const char* dir, const char* compression, uint64_t seed) {
  leveldb::Options opts;
  opts.create_if_missing = true;
  opts.error_if_exists = true;
  opts.paranoid_checks = true;
  opts.write_buffer_size = 64 << 10;
  opts.max_file_size = 256 << 10;
  opts.block_size = 4096;
  if (std::strcmp(compression, "none") == 0) {
    opts.compression = leveldb::kNoCompression;
  } else if (std::strcmp(compression, "snappy") == 0) {
    opts.compression = leveldb::kSnappyCompression;
  } else {
    std::fprintf(stderr, "leveldb-cxx: compression must be none or snappy\n");
    return 2;
  }
  leveldb::DB* db = nullptr;
  leveldb::Status st = leveldb::DB::Open(opts, dir, &db);
  if (!st.ok()) {
    return Fail("open", st);
  }
  Rng r(seed);
  Workload w(db);
  int rc = 0;
  // Round 1: every key.
  for (int i = 0; i < kKeys && rc == 0; i++) {
    if (!(st = w.Put(Key(i), Value(r, i, 1))).ok()) rc = Fail("put", st);
  }
  // Round 2: deletions and overwrites, then a full compaction.
  for (int i = 0; i < kKeys && rc == 0; i++) {
    if (i % 5 == 0) {
      if (!(st = w.Delete(Key(i))).ok()) rc = Fail("delete", st);
    } else if (i % 7 == 3) {
      if (!(st = w.Put(Key(i), Value(r, i, 2))).ok()) rc = Fail("overwrite", st);
    }
  }
  if (rc == 0) {
    db->CompactRange(nullptr, nullptr);
  }
  // Round 3: one atomic batch with new keys, deletions and a re-put of a
  // deleted key, then single writes left in the log.
  if (rc == 0) {
    for (int i = kKeys; i < kKeys + 500; i++) {
      w.BatchPut(Key(i), Value(r, i, 3));
    }
    for (int i = 0; i < kKeys; i += 11) {
      w.BatchDelete(Key(i + 4));
    }
    w.BatchPut(Key(10), Value(r, 10, 3));
    w.BatchDelete(Key(kKeys * 10));  // never existed
    if (!(st = w.Commit()).ok()) rc = Fail("batch", st);
  }
  for (int i = kKeys + 500; i < kKeys + 700 && rc == 0; i++) {
    if (!(st = w.Put(Key(i), Value(r, i, 4))).ok()) rc = Fail("put", st);
  }
  if (rc == 0 && !(st = w.Delete(Key(kKeys + 600))).ok()) {
    rc = Fail("delete", st);
  }
  delete db;
  if (rc == 0) {
    w.Print();
  }
  return rc;
}

int Dump(const char* dir) {
  leveldb::Options opts;
  opts.create_if_missing = false;
  opts.paranoid_checks = true;
  leveldb::DB* db = nullptr;
  leveldb::Status st = leveldb::DB::Open(opts, dir, &db);
  if (!st.ok()) {
    return Fail("open", st);
  }
  leveldb::ReadOptions ro;
  ro.verify_checksums = true;
  ro.fill_cache = false;
  leveldb::Iterator* it = db->NewIterator(ro);
  for (it->SeekToFirst(); it->Valid(); it->Next()) {
    std::printf("P %s %s\n", Hex(it->key().ToString()).c_str(), Hex(it->value().ToString()).c_str());
  }
  st = it->status();
  delete it;
  delete db;
  if (!st.ok()) {
    return Fail("iterate", st);
  }
  return 0;
}

int Usage() {
  std::fprintf(stderr,
               "usage: leveldb-cxx write DIR none|snappy SEED\n"
               "       leveldb-cxx dump DIR\n");
  return 2;
}

}  // namespace

int main(int argc, char** argv) {
  if (argc == 5 && std::strcmp(argv[1], "write") == 0) {
    char* end = nullptr;
    unsigned long long seed = std::strtoull(argv[4], &end, 10);
    if (end == argv[4] || *end != '\0') {
      return Usage();
    }
    return Write(argv[2], argv[3], seed);
  }
  if (argc == 3 && std::strcmp(argv[1], "dump") == 0) {
    return Dump(argv[2]);
  }
  return Usage();
}
