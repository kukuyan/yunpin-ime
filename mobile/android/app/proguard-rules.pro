# SPDX-License-Identifier: Apache-2.0
# JNI entry points are resolved by their declared Java names.
-keepclasseswithmembers,includedescriptorclasses class io.github.kukuyan.yunpin.android.ime.NativeCandidateEngine {
    native <methods>;
}

# The optional gomobile facade is discovered only after its reviewed AAR is
# added at the build gate, so release shrinking must retain that boundary.
-keep class go.** { *; }
-dontwarn go.**
