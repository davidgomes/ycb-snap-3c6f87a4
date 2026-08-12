// Copyright 2026, DragonflyDB authors.  All rights reserved.
// See LICENSE for licensing terms.
//

#include "core/search/scoring.h"

namespace dfly::search {

double ScoreDocument(ScorerFn scorer, const ScoringContext& ctx,
                     const std::vector<ScoringTermInfo>& terms) {
  double score = 0.0;
  for (const auto& term : terms)
    score += scorer(ctx, term);
  return score;
}

size_t GlobalScoringStats::TermDocFreq(std::string_view field, std::string_view term) const {
  auto it = term_doc_freq.find(std::make_pair(std::string(field), std::string(term)));
  return it != term_doc_freq.end() ? it->second : 0;
}

double GlobalScoringStats::FieldAvgLen(std::string_view field) const {
  auto it = field_avg_len.find(std::string(field));
  return it != field_avg_len.end() ? it->second : 0.0;
}

GlobalScoringStats MergeGlobalScoringStats(const std::vector<LocalScoringStats>& local_stats) {
  GlobalScoringStats global;
  for (const auto& local : local_stats)
    global.num_docs += local.num_docs;

  for (const auto& local : local_stats) {
    for (const auto& term_stat : local.term_stats)
      global.term_doc_freq[{term_stat.field, term_stat.term}] += term_stat.doc_freq;
  }

  // Sum (total_len, num_docs) per field first, then average, so shards with more matching
  // documents are weighted accordingly (an average of per-shard averages would not be).
  absl::flat_hash_map<std::string, std::pair<size_t, size_t>> field_totals;
  for (const auto& local : local_stats) {
    for (const auto& field_stat : local.field_stats) {
      auto& [total_len, num_docs] = field_totals[field_stat.field];
      total_len += field_stat.total_len;
      num_docs += field_stat.num_docs;
    }
  }
  for (const auto& [field, totals] : field_totals) {
    const auto& [total_len, num_docs] = totals;
    global.field_avg_len[field] = num_docs > 0 ? static_cast<double>(total_len) / num_docs : 0.0;
  }

  return global;
}

}  // namespace dfly::search
