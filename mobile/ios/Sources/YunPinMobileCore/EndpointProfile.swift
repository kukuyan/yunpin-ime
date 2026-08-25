// SPDX-License-Identifier: Apache-2.0

import Foundation
import Darwin

public enum EndpointProfileError: Error, Equatable, Sendable {
    case invalidDisplayName
    case invalidURL
    case credentialsOrComponentsForbidden
    case unsupportedScheme
    case privateHTTPRequiresOptIn
    case publicHTTPForbidden
}

/// A non-secret, user-selected relay profile. Device and account credentials
/// are deliberately absent and belong only in the containing app's Keychain.
public struct SyncEndpointProfile: Codable, Equatable, Sendable {
    public let id: UUID
    public let displayName: String
    public let endpoint: URL
    public let allowsPrivateHTTP: Bool

    public init(id: UUID = UUID(), displayName: String, endpoint rawEndpoint: String, allowsPrivateHTTP: Bool) throws {
        let trimmedName = displayName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedName.isEmpty,
              trimmedName.utf8.count <= 64,
              !trimmedName.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7f }) else {
            throw EndpointProfileError.invalidDisplayName
        }

        let trimmedEndpoint = rawEndpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var components = URLComponents(string: trimmedEndpoint),
              let rawScheme = components.scheme,
              let host = components.host,
              !host.isEmpty else {
            throw EndpointProfileError.invalidURL
        }
        if let port = components.port, !(1...65_535).contains(port) {
            throw EndpointProfileError.invalidURL
        }
        guard components.user == nil,
              components.password == nil,
              components.query == nil,
              components.fragment == nil,
              components.percentEncodedPath.isEmpty || components.percentEncodedPath == "/" else {
            throw EndpointProfileError.credentialsOrComponentsForbidden
        }

        let scheme = rawScheme.lowercased()
        components.scheme = scheme
        components.percentEncodedPath = ""
        switch scheme {
        case "https":
            break
        case "http":
            guard allowsPrivateHTTP else {
                throw EndpointProfileError.privateHTTPRequiresOptIn
            }
            guard Self.isPrivateIPLiteralOrLocalhost(host) else {
                throw EndpointProfileError.publicHTTPForbidden
            }
        default:
            throw EndpointProfileError.unsupportedScheme
        }

        guard let endpoint = components.url else {
            throw EndpointProfileError.invalidURL
        }
        self.id = id
        self.displayName = trimmedName
        self.endpoint = endpoint
        self.allowsPrivateHTTP = allowsPrivateHTTP
    }

    private enum CodingKeys: String, CodingKey {
        case id
        case displayName
        case endpoint
        case allowsPrivateHTTP
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let validated = try SyncEndpointProfile(
            id: container.decode(UUID.self, forKey: .id),
            displayName: container.decode(String.self, forKey: .displayName),
            endpoint: container.decode(URL.self, forKey: .endpoint).absoluteString,
            allowsPrivateHTTP: container.decode(Bool.self, forKey: .allowsPrivateHTTP)
        )
        self = validated
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        try container.encode(id, forKey: .id)
        try container.encode(displayName, forKey: .displayName)
        try container.encode(endpoint, forKey: .endpoint)
        try container.encode(allowsPrivateHTTP, forKey: .allowsPrivateHTTP)
    }

    private static func isPrivateIPLiteralOrLocalhost(_ rawHost: String) -> Bool {
        var host = rawHost.lowercased()
        if host.hasPrefix("[") && host.hasSuffix("]") {
            host.removeFirst()
            host.removeLast()
        }
        if host == "localhost" || host == "::1" {
            return true
        }
        if host.contains(":") {
            guard !host.contains("%") else { return false }
            var address = in6_addr()
            let parsed = host.withCString { inet_pton(AF_INET6, $0, &address) }
            guard parsed == 1 else { return false }
            let bytes = withUnsafeBytes(of: &address) { Array($0) }
            return bytes.count == 16 && (bytes[0] & 0xfe) == 0xfc
        }
        let octets = host.split(separator: ".", omittingEmptySubsequences: false)
        guard octets.count == 4,
              let values = try? octets.map({ component -> UInt8 in
                  guard !component.isEmpty,
                        component.allSatisfy(\.isNumber),
                        component.count == 1 || component.first != "0",
                        let value = UInt8(component) else {
                      throw EndpointProfileError.invalidURL
                  }
                  return value
              }) else {
            return false
        }
        return values[0] == 10
            || values[0] == 127
            || (values[0] == 172 && (16...31).contains(values[1]))
            || (values[0] == 192 && values[1] == 168)
    }
}
