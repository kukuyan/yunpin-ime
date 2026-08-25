// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android

import android.view.View
import android.widget.AdapterView

class SimpleItemSelectedListener(
    private val onSelected: (position: Int) -> Unit,
) : AdapterView.OnItemSelectedListener {
    override fun onItemSelected(parent: AdapterView<*>?, view: View?, position: Int, id: Long) {
        onSelected(position)
    }

    override fun onNothingSelected(parent: AdapterView<*>?) = Unit
}
