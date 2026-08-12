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

void GlobalScoringStats::Merge(const LocalScoringStats& local) {
  num_docs_ += local.num_docs;

  for (const auto& [key, local_df] : local.term_docs)
    term_docs_[key] += local_df;

  for (const auto& [field, stats] : local.field_len_stats) {
    auto& agg = field_len_stats_[field];
    agg.total_docs_len += stats.total_docs_len;
    agg.num_docs += stats.num_docs;
  }
}

size_t GlobalScoringStats::GetTermDocs(std::string_view field, std::string_view term) const {
  auto it = term_docs_.find(std::pair<std::string, std::string>(field, term));
  return it != term_docs_.end() ? it->second : 0;
}

double GlobalScoringStats::GetFieldAvgDocLen(std::string_view field) const {
  auto it = field_len_stats_.find(std::string(field));
  if (it == field_len_stats_.end() || it->second.num_docs == 0)
    return 0.0;
  return static_cast<double>(it->second.total_docs_len) / it->second.num_docs;
}

}  // namespace dfly::search
