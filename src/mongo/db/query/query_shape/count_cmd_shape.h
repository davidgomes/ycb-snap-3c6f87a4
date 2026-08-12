// Copyright (c) MongoDB, Inc.
// SPDX-License-Identifier: SSPL-1.0

#pragma once

#include "mongo/db/query/parsed_find_command.h"
#include "mongo/db/query/query_shape/query_shape.h"
#include "mongo/util/modules.h"

namespace mongo::query_shape {

struct CountCmdShapeComponents : public CmdSpecificShapeComponents {

    // The 'hasLimit' and 'hasSkip' parameters are required to initialize the hasField member
    // variable because 'request' never includes the skip or limit, even if the count command
    // contains a skip and/or limit. See the comment in parsed_find_command::parseFromCount for
    // more information.
    //
    // Similarly, 'rawData' must be passed in explicitly, since 'request' is a find command
    // synthesized from a count command and never carries the count command's own 'rawData' field.
    // It is normalized so that only an explicit value of 'true' is tracked as part of the shape -
    // both an absent 'rawData' and an explicit 'false' are treated identically (i.e. not
    // present), so that they continue to share a query shape (and hash) with count commands that
    // never mention 'rawData' at all.
    CountCmdShapeComponents(const ParsedFindCommand& request,
                            bool hasLimit,
                            bool hasSkip,
                            bool rawData = false);

    void HashValue(absl::HashState state) const final;

    size_t size() const final;

    // This anonymous struct represents the presence of the member variables as C++ bit fields.
    // In doing so, each of these boolean values takes up 1 bit instead of 1 byte.
    const struct HasField {
        bool limit : 1;
        bool skip : 1;
        // True only when 'rawData' was explicitly set to true - normalized so that an absent
        // 'rawData' and an explicit 'false' compare and hash identically.
        bool rawData : 1;
    } hasField;

    const BSONObj representativeQuery;
};

class CountCmdShape final : public Shape {
public:
    CountCmdShape(const ParsedFindCommand& find,
                 bool hasLimit,
                 bool hasSkip,
                 bool rawData = false);

    const CmdSpecificShapeComponents& specificComponents() const final;

    void appendCmdSpecificShapeComponents(
        BSONObjBuilder&,
        OperationContext*,
        const query_shape::SerializationOptions& opts) const final;

    QueryShapeHash sha256Hash(OperationContext*,
                              const SerializationContext& serializationContext) const override;

    const CountCmdShapeComponents components;
};

static_assert(sizeof(CountCmdShape) == sizeof(Shape) + sizeof(CountCmdShapeComponents),
              "If the class' members have changed, this assert and the extraSize() calculation may "
              "need to be updated with a new value.");
}  // namespace mongo::query_shape
