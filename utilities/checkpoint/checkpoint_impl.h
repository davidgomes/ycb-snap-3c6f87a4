//  Copyright (c) 2017-present, Facebook, Inc.  All rights reserved.
//  This source code is licensed under both the GPLv2 (found in the
//  COPYING file in the root directory) and Apache 2.0 License
//  (found in the LICENSE.Apache file in the root directory).

#pragma once

#include <cstdint>
#include <functional>
#include <string>
#include <unordered_map>
#include <unordered_set>
#include <vector>

#include "file/filename.h"
#include "rocksdb/db.h"
#include "rocksdb/metadata.h"
#include "rocksdb/utilities/checkpoint.h"

namespace ROCKSDB_NAMESPACE {

class CopyEngine;
class RateLimiter;

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
  // staging dir is committed. An empty `column_families` checkpoints all
  // column families; otherwise only those (plus default) are included.
  Status CreateCheckpointImpl(
      const std::string& checkpoint_dir,
      const std::vector<ColumnFamilyHandle*>& column_families,
      uint64_t log_size_for_flush, uint64_t* sequence_number_ptr,
      CopyEngine* engine, bool use_link, RateLimiter* copy_rate_limiter);

  Status ExportColumnFamily(ColumnFamilyHandle* handle,
                            const std::string& export_dir,
                            ExportImportFilesMetaData** metadata) override;

  // Checkpoint logic can be customized by providing callbacks for link, copy,
  // or create. If set, preprocess_files_cb is called with the live files
  // before any other callback; it may remove entries (e.g. files it handled
  // itself or that should be left out) and its error aborts the checkpoint.
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
      const std::function<Status(std::vector<LiveFileStorageInfo>* files)>&
          preprocess_files_cb = nullptr);

 private:
  Status CleanStagingDirectory(const std::string& path, Logger* info_log);

  // Validates `column_families` and returns the deduplicated handles, plus
  // the default column family, ordered by column family id.
  Status ResolveColumnFamilySubset(
      const std::vector<ColumnFamilyHandle*>& column_families,
      std::vector<ColumnFamilyHandle*>* included);

  // Writes MANIFEST and OPTIONS files describing only `included` column
  // families into staging_dir, and removes those files plus the table/blob
  // files of the other column families from `files`.
  Status PrepareColumnFamilySubset(
      const std::vector<ColumnFamilyHandle*>& included,
      const std::string& staging_dir, const DBOptions& db_options,
      std::vector<LiveFileStorageInfo>* files);

  // Rewrites the first manifest.size bytes of the live MANIFEST to dst_path,
  // appending drop records for every column family not in included_cf_ids.
  // Fills *file_to_cf with the column family of each table/blob file added.
  Status WriteSubsetManifest(
      const LiveFileStorageInfo& manifest,
      const std::unordered_set<uint32_t>& included_cf_ids,
      const std::string& dst_path, const DBOptions& db_options,
      std::unordered_map<uint64_t, uint32_t>* file_to_cf);

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
