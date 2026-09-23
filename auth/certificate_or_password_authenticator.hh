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
class certificate_authenticator;
class password_authenticator;

///
/// Authenticates a CQL connection from a trusted TLS client certificate when one
/// is presented, and otherwise from a username/password SASL exchange.
///
/// A presented certificate that cannot be mapped to a role fails the connection.
/// Password authentication is not attempted in that case.
///
class certificate_or_password_authenticator : public authenticator {
    std::unique_ptr<certificate_authenticator> _certificate;
    std::unique_ptr<password_authenticator> _password;

public:
    certificate_or_password_authenticator(cql3::query_processor&, ::service::raft_group0_client&, ::service::migration_manager&, cache&, const config&);
    ~certificate_or_password_authenticator();

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
