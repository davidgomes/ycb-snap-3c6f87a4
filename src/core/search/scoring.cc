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

void GlobalScoringStats::Merge(GlobalScoringStats&& other) {
  num_docs += other.num_docs;
  for (auto& [key, docs] : other.term_docs)
    term_docs[key] += docs;
  for (auto& [field, lens] : other.field_lens) {
    auto& dest = field_lens[field];
    dest.total_len += lens.total_len;
    dest.num_docs += lens.num_docs;
  }
}

}  // namespace dfly::search
