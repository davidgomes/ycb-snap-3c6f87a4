/*
 * Copyright (C) 2026-present ScyllaDB
 */

/*
 * SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
 */

#pragma once

#include "auth/authenticator.hh"

#include <memory>

namespace cql3 {
class query_processor;
}

namespace service {
class migration_manager;
class raft_group0_client;
}

namespace auth {

class cache;
struct config;

///
/// Authenticates a CQL connection from a trusted TLS client certificate when one
/// is presented, and from a username/password SASL exchange otherwise.
///
/// A presented certificate stays on the certificate path: failure to map it to a
/// role (via `auth_certificate_role_queries`) is an authentication failure.
/// Password SASL is used only when the peer presents no certificate, which is
/// the case for optional client-auth TLS and for a plain non-TLS connection.
/// Untrusted certificates are rejected by TLS verification before this runs.
///
class certificate_or_password_authenticator : public authenticator {
    std::unique_ptr<authenticator> _certificate;
    std::unique_ptr<authenticator> _password;

public:
    certificate_or_password_authenticator(cql3::query_processor&, ::service::raft_group0_client&, ::service::migration_manager&, cache&, const config&);

    future<> start() override;
    future<> stop() override;

    std::string_view qualified_java_name() const override;

    bool require_authentication() const override;

    authentication_option_set supported_options() const override;
    authentication_option_set alterable_options() const override;

    future<authenticated_user> authenticate(const credentials_map& credentials) const override;
    future<std::optional<authenticated_user>> authenticate(session_dn_func) const override;

    future<> create(std::string_view role_name, const authentication_options& options, ::service::group0_batch& mc) override;
    future<> alter(std::string_view role_name, const authentication_options& options, ::service::group0_batch&) override;
    future<> drop(std::string_view role_name, ::service::group0_batch&) override;

    future<custom_options> query_custom_options(std::string_view role_name) const override;

    bool uses_password_hashes() const override;
    future<std::optional<sstring>> get_password_hash(std::string_view role_name) const override;

    const resource_set& protected_resources() const override;

    ::shared_ptr<sasl_challenge> new_sasl_challenge() const override;

    future<> ensure_superuser_is_created() const override;
};

}
