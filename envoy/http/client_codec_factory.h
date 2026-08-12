#pragma once

#include "envoy/http/codec.h"
#include "envoy/network/connection.h"
#include "envoy/network/transport_socket.h"

namespace Envoy {
namespace Upstream {
class ClusterInfo;
} // namespace Upstream

namespace Http {

class ClientCodecFactory {
public:
  virtual ~ClientCodecFactory() = default;

  struct Context {
    CodecType type;
    Network::Connection& connection;
    ConnectionCallbacks& callbacks;
    const Upstream::ClusterInfo& cluster;
    Random::RandomGenerator& random;
    const std::shared_ptr<const Network::TransportSocketOptions>& options;
  };

  /**
   * Create a client codec for the given context.
   * @param context the context to use for creating the codec.
   * @return ClientConnectionPtr the client codec, or nullptr to use the stock codec.
   */
  virtual ClientConnectionPtr createClientCodec(const Context& context) const PURE;
};

} // namespace Http
} // namespace Envoy
