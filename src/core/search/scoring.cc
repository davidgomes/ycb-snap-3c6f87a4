// Copyright 2026, DragonflyDB authors.  All rights reserved.
// See LICENSE for licensing terms.
//

#include "core/search/scoring.h"

namespace dfly::search {

void TextScoringStats::Merge(const TextScoringStats& other) {
  num_docs += other.num_docs;
  for (const auto& [field, stats] : other.fields) {
    auto& dst = fields[field];
    dst.total_len += stats.total_len;
    dst.num_docs += stats.num_docs;
  }
  for (const auto& [key, docs] : other.term_docs)
    term_docs[key] += docs;
}

double ScoreDocument(ScorerFn scorer, const ScoringContext& ctx,
                     const std::vector<ScoringTermInfo>& terms) {
  double score = 0.0;
  for (const auto& term : terms)
    score += scorer(ctx, term);
  return score;
}

}  // namespace dfly::search
