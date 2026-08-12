/*
 * Copyright (C) 2026-present ScyllaDB
 *
 */

/*
 * SPDX-License-Identifier: LicenseRef-ScyllaDB-Source-Available-1.1
 */

#include "auth/certificate_or_password_authenticator.hh"

#include <utility>

namespace auth {

constexpr std::string_view certificate_or_password_authenticator_name("com.scylladb.auth.CertificateOrPasswordAuthenticator");

certificate_or_password_authenticator::certificate_or_password_authenticator(
        cql3::query_processor& qp,
        ::service::raft_group0_client& group0_client,
        ::service::migration_manager& migration_manager,
        cache& cache,
        const config& cfg)
    : password_authenticator(qp, group0_client, migration_manager, cache, cfg)
    , _certificate_authenticator(qp, group0_client, migration_manager, cache, cfg)
{}

std::string_view certificate_or_password_authenticator::qualified_java_name() const {
    return certificate_or_password_authenticator_name;
}

future<std::optional<authenticated_user>> certificate_or_password_authenticator::authenticate(session_dn_func get_certificate) const {
    return _certificate_authenticator.authenticate_if_present(std::move(get_certificate));
}

}
