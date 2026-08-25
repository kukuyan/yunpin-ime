// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import android.content.ContentProvider
import android.content.ContentValues
import android.database.Cursor
import android.net.Uri
import android.os.Bundle
import io.github.kukuyan.yunpin.android.config.ProfileSwitchBarrierProtocol

class ProfileSwitchBarrierProvider : ContentProvider() {
    override fun onCreate(): Boolean = true

    override fun call(method: String, arg: String?, extras: Bundle?): Bundle? {
        when (method) {
            ProfileSwitchBarrierProtocol.BEGIN -> ImeProfileRuntime.beginSwitch()
            ProfileSwitchBarrierProtocol.FINISH -> ImeProfileRuntime.finishSwitch()
            else -> return null
        }
        return Bundle().apply { putBoolean(ProfileSwitchBarrierProtocol.ACKNOWLEDGED, true) }
    }

    override fun query(
        uri: Uri,
        projection: Array<out String>?,
        selection: String?,
        selectionArgs: Array<out String>?,
        sortOrder: String?,
    ): Cursor? = null

    override fun getType(uri: Uri): String? = null
    override fun insert(uri: Uri, values: ContentValues?): Uri? = null
    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?): Int = 0
    override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?): Int = 0
}
