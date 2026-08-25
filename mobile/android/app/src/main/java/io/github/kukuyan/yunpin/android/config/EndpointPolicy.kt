// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import java.net.InetAddress
import java.net.URI
import java.util.Locale

data class EndpointConfig(
    val normalizedEndpoint: String,
    val allowPrivateHttp: Boolean,
)

class EndpointValidationException(message: String) : IllegalArgumentException(message)

object EndpointPolicy {
    fun parse(raw: String, allowPrivateHttp: Boolean): EndpointConfig {
        val trimmed = raw.trim()
        val uri = try {
            URI(trimmed)
        } catch (_: Exception) {
            throw EndpointValidationException("The sync endpoint must be an absolute HTTP(S) URL")
        }
        val scheme = uri.scheme?.lowercase(Locale.ROOT)
            ?: throw EndpointValidationException("The sync endpoint must be absolute")
        val host = uri.host?.removePrefix("[")?.removeSuffix("]")
            ?: throw EndpointValidationException("The sync endpoint requires a host")
        if (uri.userInfo != null || uri.rawQuery != null || uri.rawFragment != null ||
            uri.rawPath !in setOf(null, "", "/") || uri.rawAuthority?.contains('@') == true
        ) {
            throw EndpointValidationException("Credentials, paths, queries, and fragments are not allowed")
        }
        if (uri.port !in -1..65535 || uri.port == 0) {
            throw EndpointValidationException("The sync endpoint port is invalid")
        }
        when (scheme) {
            "https" -> Unit
            "http" -> {
                if (!allowPrivateHttp) {
                    throw EndpointValidationException("Private HTTP requires explicit opt-in")
                }
                if (!isPrivateLiteralOrLocalhost(host)) {
                    throw EndpointValidationException("HTTP is restricted to localhost or a private IP literal")
                }
            }
            else -> throw EndpointValidationException("Only HTTP and HTTPS are supported")
        }
        val normalized = URI(
            scheme,
            null,
            host.lowercase(Locale.ROOT),
            uri.port,
            null,
            null,
            null,
        ).toASCIIString()
        return EndpointConfig(normalized, allowPrivateHttp)
    }

    private fun isPrivateLiteralOrLocalhost(host: String): Boolean {
        if (host.equals("localhost", ignoreCase = true)) return true
        val ipv4 = parseIpv4(host)
        if (ipv4 != null) {
            return ipv4[0] == 10 ||
                ipv4[0] == 127 ||
                (ipv4[0] == 172 && ipv4[1] in 16..31) ||
                (ipv4[0] == 192 && ipv4[1] == 168)
        }
        if (!host.contains(':') || !host.matches(Regex("[0-9A-Fa-f:.]+"))) return false
        val bytes = try {
            InetAddress.getByName(host).address
        } catch (_: Exception) {
            return false
        }
        return bytes.size == 16 &&
            (bytes.all { it == 0.toByte() }.not()) &&
            ((bytes[0].toInt() and 0xfe) == 0xfc || InetAddress.getByAddress(bytes).isLoopbackAddress)
    }

    private fun parseIpv4(host: String): IntArray? {
        val parts = host.split('.')
        if (parts.size != 4) return null
        val values = IntArray(4)
        for ((index, part) in parts.withIndex()) {
            if (part.isEmpty() || part.length > 3 || part.any { !it.isDigit() }) return null
            if (part.length > 1 && part.startsWith('0')) return null
            val value = part.toIntOrNull() ?: return null
            if (value !in 0..255) return null
            values[index] = value
        }
        return values
    }
}
