// Copyright (C) 2019-2020 Zilliz. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software distributed under the License
// is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express
// or implied. See the License for the specific language governing permissions and limitations under the License

#pragma once
#include <algorithm>
#include <cmath>
#include <limits>
#include <memory>
#include <optional>
#include "common/EasyAssert.h"
#include "common/JsonCastType.h"
#include "common/Types.h"
#include "index/Index.h"
#include "index/InvertedIndexTantivy.h"
#include "index/InvertedIndexUtil.h"
#include "index/ScalarIndex.h"
#include "log/Log.h"
namespace milvus::index {

class JsonFlatIndex;
// JsonFlatIndexQueryExecutor is used to execute queries on a specified json path, and can be constructed by JsonFlatIndex
template <typename T>
class JsonFlatIndexQueryExecutor : public InvertedIndexTantivy<T> {
 public:
    JsonFlatIndexQueryExecutor(std::string& json_path,
                               const JsonFlatIndex& json_flat_index);

    ~JsonFlatIndexQueryExecutor() {
        this->wrapper_ = nullptr;
    }

    const TargetBitmap
    In(size_t n, const T* values) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::In",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_terms_query(json_path_, values, n, &bitset);
        return bitset;
    }

    TargetBitmap
    Exists() override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::Exists",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_exist_query(json_path_, &bitset);
        return bitset;
    }

    TargetBitmap
    IsNotNull() override {
        TargetBitmap bitset(this->Count());
        if constexpr ((std::is_integral_v<T> && !std::is_same_v<T, bool>) ||
                      std::is_floating_point_v<T>) {
            this->wrapper_->json_range_query(json_path_,
                                             std::numeric_limits<int64_t>::min(),
                                             std::numeric_limits<int64_t>::max(),
                                             false,
                                             false,
                                             true,
                                             true,
                                             &bitset);
            this->wrapper_->json_range_query(
                json_path_,
                uint64_t{},
                std::numeric_limits<uint64_t>::max(),
                false,
                false,
                true,
                true,
                &bitset);
            this->wrapper_->json_range_query(json_path_,
                                             std::numeric_limits<double>::lowest(),
                                             std::numeric_limits<double>::max(),
                                             false,
                                             false,
                                             true,
                                             true,
                                             &bitset);
        } else if constexpr (std::is_same_v<T, bool>) {
            const bool values[] = {false, true};
            this->wrapper_->json_terms_query(
                json_path_, values, 2, &bitset);
        } else if constexpr (std::is_same_v<T, std::string>) {
            this->wrapper_->json_prefix_query(json_path_, "", &bitset);
        } else {
            static_assert(sizeof(T) == 0,
                          "unsupported JSON flat index value type");
        }
        auto field_valid = InvertedIndexTantivy<T>::IsNotNull();
        bitset &= field_valid;
        return bitset;
    }

    const TargetBitmap
    InApplyFilter(
        size_t n,
        const T* values,
        const std::function<bool(size_t /* offset */)>& filter) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::InApplyFilter",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_terms_query(json_path_, values, n, &bitset);
        apply_hits_with_filter(bitset, filter);
        return bitset;
    }

    virtual void
    InApplyCallback(
        size_t n,
        const T* values,
        const std::function<void(size_t /* offset */)>& callback) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::InApplyCallback",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_terms_query(json_path_, values, n, &bitset);
        apply_hits_with_callback(bitset, callback);
    }

    const TargetBitmap
    NotIn(size_t n, const T* values) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::NotIn",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_terms_query(json_path_, values, n, &bitset);

        bitset.flip();

        // TODO: optimize this
        auto null_bitset = this->IsNotNull();
        bitset &= null_bitset;

        return bitset;
    }

    const TargetBitmap
    Range(const T& value, OpType op) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::Range",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        QueryRangeForType(value, op, bitset);
        if constexpr (std::is_integral_v<T> && !std::is_same_v<T, bool>) {
            const auto numeric_value = static_cast<double>(value);
            QueryRangeForType(numeric_value, op, bitset);
            QueryUnsignedIntegerRangeForSigned(value, op, bitset);
        } else if constexpr (std::is_floating_point_v<T>) {
            switch (op) {
                case OpType::LessThan:
                    QueryIntegerRange(
                        std::nullopt, false, value, false, bitset);
                    QueryUnsignedIntegerRange(
                        std::nullopt, false, value, false, bitset);
                    break;
                case OpType::LessEqual:
                    QueryIntegerRange(
                        std::nullopt, false, value, true, bitset);
                    QueryUnsignedIntegerRange(
                        std::nullopt, false, value, true, bitset);
                    break;
                case OpType::GreaterThan:
                    QueryIntegerRange(
                        value, false, std::nullopt, false, bitset);
                    QueryUnsignedIntegerRange(
                        value, false, std::nullopt, false, bitset);
                    break;
                case OpType::GreaterEqual:
                    QueryIntegerRange(
                        value, true, std::nullopt, false, bitset);
                    QueryUnsignedIntegerRange(
                        value, true, std::nullopt, false, bitset);
                    break;
                default:
                    ThrowInfo(OpTypeInvalid,
                              fmt::format("Invalid OperatorType: {}", op));
            }
        }
        return bitset;
    }

    const TargetBitmap
    Query(const DatasetPtr& dataset) override {
        return InvertedIndexTantivy<T>::Query(dataset);
    }

    const TargetBitmap
    Range(const T& lower_bound_value,
          bool lb_inclusive,
          const T& upper_bound_value,
          bool ub_inclusive) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::RangeWithBounds",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_range_query(json_path_,
                                         lower_bound_value,
                                         upper_bound_value,
                                         false,
                                         false,
                                         lb_inclusive,
                                         ub_inclusive,
                                         &bitset);
        if constexpr (std::is_integral_v<T> && !std::is_same_v<T, bool>) {
            const auto lower = static_cast<double>(lower_bound_value);
            const auto upper = static_cast<double>(upper_bound_value);
            this->wrapper_->json_range_query(
                json_path_,
                lower,
                upper,
                false,
                false,
                lb_inclusive,
                ub_inclusive,
                &bitset);
            QueryUnsignedIntegerRangeForSignedBounds(lower_bound_value,
                                                     lb_inclusive,
                                                     upper_bound_value,
                                                     ub_inclusive,
                                                     bitset);
        } else if constexpr (std::is_floating_point_v<T>) {
            QueryIntegerRange(lower_bound_value,
                              lb_inclusive,
                              upper_bound_value,
                              ub_inclusive,
                              bitset);
            QueryUnsignedIntegerRange(lower_bound_value,
                                      lb_inclusive,
                                      upper_bound_value,
                                      ub_inclusive,
                                      bitset);
        }
        return bitset;
    }

    const TargetBitmap
    PrefixMatch(const std::string_view prefix) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::PrefixMatch",
                              tracer::GetRootSpan());
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_prefix_query(
            json_path_, std::string(prefix), &bitset);
        return bitset;
    }

 protected:
    const TargetBitmap
    PatternQuery(const std::string& pattern) override {
        tracer::AutoSpan span("JsonFlatIndexQueryExecutor::PatternQuery",
                              tracer::GetRootSpan());
        PatternMatchTranslator translator;
        auto regex_pattern = translator(pattern);
        TargetBitmap bitset(this->Count());
        this->wrapper_->json_regex_query(json_path_, regex_pattern, &bitset);
        return bitset;
    }

 private:
    template <typename ValueType>
    void
    QueryRangeForType(const ValueType& value,
                      OpType op,
                      TargetBitmap& bitset) {
        switch (op) {
            case OpType::LessThan:
                this->wrapper_->json_range_query(
                    json_path_,
                    ValueType{},
                    value,
                    true,
                    false,
                    false,
                    false,
                    &bitset);
                break;
            case OpType::LessEqual:
                this->wrapper_->json_range_query(
                    json_path_,
                    ValueType{},
                    value,
                    true,
                    false,
                    true,
                    false,
                    &bitset);
                break;
            case OpType::GreaterThan:
                this->wrapper_->json_range_query(json_path_,
                                                 value,
                                                 ValueType{},
                                                 false,
                                                 true,
                                                 false,
                                                 false,
                                                 &bitset);
                break;
            case OpType::GreaterEqual:
                this->wrapper_->json_range_query(json_path_,
                                                 value,
                                                 ValueType{},
                                                 false,
                                                 true,
                                                 true,
                                                 false,
                                                 &bitset);
                break;
            default:
                ThrowInfo(OpTypeInvalid,
                          fmt::format("Invalid OperatorType: {}", op));
        }
    }

    void
    QueryIntegerRange(std::optional<double> lower,
                      bool lower_inclusive,
                      std::optional<double> upper,
                      bool upper_inclusive,
                      TargetBitmap& bitset) {
        constexpr auto min = std::numeric_limits<int64_t>::min();
        constexpr auto max = std::numeric_limits<int64_t>::max();
        const auto min_ld = static_cast<long double>(min);
        const auto max_ld = static_cast<long double>(max);
        int64_t lower_value = min;
        int64_t upper_value = max;
        bool lower_unbounded = true;
        bool upper_unbounded = true;

        if (lower.has_value()) {
            const auto value = static_cast<long double>(lower.value());
            if (std::isnan(lower.value()) ||
                (lower_inclusive ? value > max_ld : value >= max_ld)) {
                return;
            }
            if (lower_inclusive ? value > min_ld : value >= min_ld) {
                lower_unbounded = false;
                lower_value = lower_inclusive
                                  ? static_cast<int64_t>(std::ceil(value))
                                  : static_cast<int64_t>(std::floor(value));
            }
        }
        if (upper.has_value()) {
            const auto value = static_cast<long double>(upper.value());
            if (std::isnan(upper.value()) ||
                (upper_inclusive ? value < min_ld : value <= min_ld)) {
                return;
            }
            if (upper_inclusive ? value < max_ld : value <= max_ld) {
                upper_unbounded = false;
                upper_value = upper_inclusive
                                  ? static_cast<int64_t>(std::floor(value))
                                  : static_cast<int64_t>(std::ceil(value));
            }
        }

        const auto first =
            lower_unbounded
                ? min_ld
                : static_cast<long double>(lower_value) +
                      (lower_inclusive ? 0.0L : 1.0L);
        const auto last =
            upper_unbounded
                ? max_ld
                : static_cast<long double>(upper_value) -
                      (upper_inclusive ? 0.0L : 1.0L);
        if (first > last) {
            return;
        }
        this->wrapper_->json_range_query(json_path_,
                                         lower_value,
                                         upper_value,
                                         lower_unbounded,
                                         upper_unbounded,
                                         lower_inclusive,
                                         upper_inclusive,
                                         &bitset);
    }

    void
    QueryUnsignedIntegerRangeForSigned(int64_t value,
                                       OpType op,
                                       TargetBitmap& bitset) {
        if (value < 0) {
            if (op == OpType::GreaterThan || op == OpType::GreaterEqual) {
                this->wrapper_->json_range_query(
                    json_path_,
                    uint64_t{},
                    std::numeric_limits<uint64_t>::max(),
                    false,
                    false,
                    true,
                    true,
                    &bitset);
            }
            return;
        }
        auto unsigned_value = static_cast<uint64_t>(value);
        switch (op) {
            case OpType::LessThan:
                this->wrapper_->json_range_query(json_path_,
                                                 uint64_t{},
                                                 unsigned_value,
                                                 true,
                                                 false,
                                                 false,
                                                 false,
                                                 &bitset);
                break;
            case OpType::LessEqual:
                this->wrapper_->json_range_query(json_path_,
                                                 uint64_t{},
                                                 unsigned_value,
                                                 true,
                                                 false,
                                                 false,
                                                 true,
                                                 &bitset);
                break;
            case OpType::GreaterThan:
                this->wrapper_->json_range_query(json_path_,
                                                 unsigned_value,
                                                 uint64_t{},
                                                 false,
                                                 true,
                                                 false,
                                                 false,
                                                 &bitset);
                break;
            case OpType::GreaterEqual:
                this->wrapper_->json_range_query(json_path_,
                                                 unsigned_value,
                                                 uint64_t{},
                                                 false,
                                                 true,
                                                 true,
                                                 false,
                                                 &bitset);
                break;
            default:
                ThrowInfo(OpTypeInvalid,
                          fmt::format("Invalid OperatorType: {}", op));
        }
    }

    void
    QueryUnsignedIntegerRangeForSignedBounds(int64_t lower,
                                             bool lower_inclusive,
                                             int64_t upper,
                                             bool upper_inclusive,
                                             TargetBitmap& bitset) {
        if (upper < 0) {
            return;
        }
        const bool lower_unbounded = lower < 0;
        this->wrapper_->json_range_query(
            json_path_,
            lower_unbounded ? uint64_t{} : static_cast<uint64_t>(lower),
            static_cast<uint64_t>(upper),
            lower_unbounded,
            false,
            lower_inclusive,
            upper_inclusive,
            &bitset);
    }

    void
    QueryUnsignedIntegerRange(std::optional<double> lower,
                              bool lower_inclusive,
                              std::optional<double> upper,
                              bool upper_inclusive,
                              TargetBitmap& bitset) {
        constexpr uint64_t min = 0;
        constexpr auto max = std::numeric_limits<uint64_t>::max();
        const auto min_ld = static_cast<long double>(min);
        const auto max_ld = static_cast<long double>(max);
        uint64_t lower_value = min;
        uint64_t upper_value = max;
        bool lower_unbounded = true;
        bool upper_unbounded = true;

        if (lower.has_value()) {
            const auto value = static_cast<long double>(lower.value());
            if (std::isnan(lower.value()) ||
                (lower_inclusive ? value > max_ld : value >= max_ld)) {
                return;
            }
            if (lower_inclusive ? value > min_ld : value >= min_ld) {
                lower_unbounded = false;
                lower_value = lower_inclusive
                                  ? static_cast<uint64_t>(std::ceil(value))
                                  : static_cast<uint64_t>(std::floor(value));
            }
        }
        if (upper.has_value()) {
            const auto value = static_cast<long double>(upper.value());
            if (std::isnan(upper.value()) ||
                (upper_inclusive ? value < min_ld : value <= min_ld)) {
                return;
            }
            if (upper_inclusive ? value < max_ld : value <= max_ld) {
                upper_unbounded = false;
                upper_value = upper_inclusive
                                  ? static_cast<uint64_t>(std::floor(value))
                                  : static_cast<uint64_t>(std::ceil(value));
            }
        }

        const auto first =
            lower_unbounded
                ? min_ld
                : static_cast<long double>(lower_value) +
                      (lower_inclusive ? 0.0L : 1.0L);
        const auto last =
            upper_unbounded
                ? max_ld
                : static_cast<long double>(upper_value) -
                      (upper_inclusive ? 0.0L : 1.0L);
        if (first > last) {
            return;
        }
        this->wrapper_->json_range_query(json_path_,
                                         lower_value,
                                         upper_value,
                                         lower_unbounded,
                                         upper_unbounded,
                                         lower_inclusive,
                                         upper_inclusive,
                                         &bitset);
    }

    std::string json_path_;
};

// JsonFlatIndex is not bound to any specific type,
// we need to reuse InvertedIndexTantivy's Build and Load implementation, so we specify the template parameter as std::string
// JsonFlatIndex should not be used to execute queries, use JsonFlatIndexQueryExecutor instead
class JsonFlatIndex : public InvertedIndexTantivy<std::string> {
    template <typename T>
    friend class JsonFlatIndexQueryExecutor;

 public:
    JsonFlatIndex() : InvertedIndexTantivy<std::string>() {
    }

    explicit JsonFlatIndex(
        const storage::FileManagerContext& ctx,
        const std::string& nested_path,
        const int64_t tantivy_index_version = TANTIVY_INDEX_LATEST_VERSION)
        : InvertedIndexTantivy<std::string>(
              tantivy_index_version, ctx, false, false),
          nested_path_(nested_path) {
    }

    void
    build_index_for_json(const std::vector<std::shared_ptr<FieldDataBase>>&
                             field_datas) override;

    template <typename T>
    std::shared_ptr<JsonFlatIndexQueryExecutor<T>>
    create_executor(std::string json_path) const {
        // json path should be in the format of /a/b/c, we need to convert it to tantivy path like a.b.c
        std::replace(json_path.begin(), json_path.end(), '/', '.');
        if (!json_path.empty()) {
            json_path = json_path.substr(1);
        }

        return std::make_shared<JsonFlatIndexQueryExecutor<T>>(json_path,
                                                               *this);
    }

    JsonCastType
    GetCastType() const override {
        return JsonCastType::FromString("JSON");
    }

    std::string
    GetNestedPath() const {
        return nested_path_;
    }

    void
    finish() {
        this->wrapper_->finish();
    }

    void
    create_reader(SetBitsetFn set_bitset) {
        this->wrapper_->create_reader(set_bitset);
    }

 private:
    std::string nested_path_;
};

template <typename T>
JsonFlatIndexQueryExecutor<T>::JsonFlatIndexQueryExecutor(
    std::string& json_path, const JsonFlatIndex& json_flat_index) {
    json_path_ = json_path;
    this->wrapper_ = json_flat_index.wrapper_;
    this->null_offset_ = json_flat_index.null_offset_;
}
}  // namespace milvus::index
