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

///
/// Authenticates clients that present a TLS client certificate the same way as certificate_authenticator,
/// and all other clients (TLS without a client certificate, or unencrypted) with username/password like
/// password_authenticator. This lets certificate and password clients share a CQL port.
///
/// Once a client has presented a certificate, only that certificate is used: if no role can be extracted
/// from it, authentication fails rather than falling back to a password.
///
class certificate_or_password_authenticator : public password_authenticator {
    certificate_authenticator _certificate_authenticator;
public:
    certificate_or_password_authenticator(cql3::query_processor&, ::service::raft_group0_client&, ::service::migration_manager&, cache&, const config&);
    ~certificate_or_password_authenticator();

    future<> start() override;
    future<> stop() override;

    std::string_view qualified_java_name() const override;

    using password_authenticator::authenticate;
    future<std::optional<authenticated_user>> authenticate(session_dn_func) const override;
};

}
