/*
 * Copyright (C) 2026-present ScyllaDB
 *
 */

/*
 * SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
 */

#pragma once

#include "auth/certificate_authenticator.hh"
#include "auth/password_authenticator.hh"

namespace auth {

extern const std::string_view certificate_or_password_authenticator_name;

class certificate_or_password_authenticator final : public password_authenticator {
    certificate_authenticator _certificate_authenticator;

public:
    certificate_or_password_authenticator(cql3::query_processor&, ::service::raft_group0_client&,
            ::service::migration_manager&, cache&, const config&);

    std::string_view qualified_java_name() const override;

    future<std::optional<authenticated_user>> authenticate(session_dn_func) const override;
};

}
