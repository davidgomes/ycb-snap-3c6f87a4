//  Copyright (c) 2017-present, Facebook, Inc.  All rights reserved.
//  This source code is licensed under both the GPLv2 (found in the
//  COPYING file in the root directory) and Apache 2.0 License
//  (found in the LICENSE.Apache file in the root directory).

#pragma once

#include <cstdint>
#include <string>
#include <unordered_set>
#include <vector>

#include "file/filename.h"
#include "rocksdb/db.h"
#include "rocksdb/utilities/checkpoint.h"

namespace ROCKSDB_NAMESPACE {

class CopyEngine;
class RateLimiter;

// Describes MANIFEST edits for a column-family subset checkpoint. When
// column_families_to_drop is non-empty, drop records are appended to the
// copied MANIFEST before the checkpoint directory is published. This does not
// modify the source DB.
struct CheckpointSubsetManifest {
  std::string manifest_filename;
  uint64_t manifest_size = 0;
  std::vector<uint32_t> column_families_to_drop;
};

class CheckpointImpl : public Checkpoint {
 public:
  explicit CheckpointImpl(DB* db) : db_(db) {}

  Status CreateCheckpoint(const std::string& checkpoint_dir,
                          uint64_t log_size_for_flush,
                          uint64_t* sequence_number_ptr) override;

  Status CreateCheckpoint(
      const std::string& checkpoint_dir,
      const std::vector<ColumnFamilyHandle*>& column_families,
      uint64_t log_size_for_flush, uint64_t* sequence_number_ptr) override;

  // Shared by the legacy Checkpoint API and CheckpointEngine. engine == nullptr
  // links/copies serially; otherwise work runs on the pool, awaited before the
  // staging dir is committed.
  // included_cf_ids == nullptr checkpoints every column family. Otherwise only
  // those ids (the default column family id must be present) are kept.
  Status CreateCheckpointImpl(
      const std::string& checkpoint_dir, uint64_t log_size_for_flush,
      uint64_t* sequence_number_ptr, CopyEngine* engine, bool use_link,
      RateLimiter* copy_rate_limiter,
      const std::unordered_set<uint32_t>* included_cf_ids = nullptr);

  Status ExportColumnFamily(ColumnFamilyHandle* handle,
                            const std::string& export_dir,
                            ExportImportFilesMetaData** metadata) override;

  // Checkpoint logic can be customized by providing callbacks for link, copy,
  // or create.
  Status CreateCustomCheckpoint(
      std::function<Status(const std::string& src_dirname,
                           const std::string& fname, FileType type,
                           const Temperature temperature)>
          link_file_cb,
      std::function<Status(const std::string& src_dirname,
                           const std::string& fname, uint64_t size_limit_bytes,
                           FileType type, const std::string& checksum_func_name,
                           const std::string& checksum_val,
                           const Temperature src_temperature)>
          copy_file_cb,
      std::function<Status(const std::string& fname,
                           const std::string& contents, FileType type)>
          create_file_cb,
      uint64_t* sequence_number, uint64_t log_size_for_flush,
      bool get_live_table_checksum = false, bool atomic_flush = false,
      const std::unordered_set<uint32_t>* included_cf_ids = nullptr,
      CheckpointSubsetManifest* subset_manifest = nullptr);

 private:
  Status CleanStagingDirectory(const std::string& path, Logger* info_log);

  // Export logic customization by providing callbacks for link or copy.
  Status ExportFilesInMetaData(
      const DBOptions& db_options, const ColumnFamilyMetaData& metadata,
      std::function<Status(const std::string& src_dirname,
                           const std::string& fname)>
          link_file_cb,
      std::function<Status(const std::string& src_dirname,
                           const std::string& fname)>
          copy_file_cb);

 private:
  DB* db_;
};

}  // namespace ROCKSDB_NAMESPACE
