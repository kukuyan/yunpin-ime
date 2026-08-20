// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

/** Prevents an existing credential namespace from being rebound to a server. */
internal object ServerProfileMutationPolicy {
    const val ENDPOINT_IMMUTABLE_MESSAGE =
        "The server endpoint is immutable; create a new profile and use a separate approved credential"

    fun requireEndpointUnchanged(existing: EndpointConfig?, requested: EndpointConfig) {
        if (existing != null && existing != requested) {
            throw EndpointValidationException(ENDPOINT_IMMUTABLE_MESSAGE)
        }
    }
}
