// Copyright 2026, DragonflyDB authors.  All rights reserved.
// See LICENSE for licensing terms.
//

#include "core/search/scoring.h"

#include <string>

namespace dfly::search {

double ScoreDocument(ScorerFn scorer, const ScoringContext& ctx,
                     const std::vector<ScoringTermInfo>& terms) {
  double score = 0.0;
  for (const auto& term : terms)
    score += scorer(ctx, term);
  return score;
}

void ScoringCorpusStats::Merge(const ScoringCorpusStats& other) {
  num_docs += other.num_docs;
  for (const auto& [field, st] : other.fields) {
    auto& dst = fields[field];
    dst.total_len += st.total_len;
    dst.num_docs += st.num_docs;
  }
  for (const auto& [field, terms] : other.term_df) {
    auto& dst_terms = term_df[field];
    for (const auto& [term, df] : terms)
      dst_terms[term] += df;
  }
}

double ScoringCorpusStats::FieldAvgDocLen(std::string_view field) const {
  auto it = fields.find(std::string(field));
  if (it == fields.end() || it->second.num_docs == 0)
    return 0.0;
  return static_cast<double>(it->second.total_len) / it->second.num_docs;
}

size_t ScoringCorpusStats::TermDf(std::string_view field, std::string_view term) const {
  auto fit = term_df.find(std::string(field));
  if (fit == term_df.end())
    return 0;
  auto tit = fit->second.find(std::string(term));
  return tit == fit->second.end() ? 0 : tit->second;
}

}  // namespace dfly::search
