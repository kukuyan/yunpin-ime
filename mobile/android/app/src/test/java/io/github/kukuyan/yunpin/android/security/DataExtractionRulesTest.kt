// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.security

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue
import org.junit.Test

class DataExtractionRulesTest {
    @Test
    fun cloudBackupAndDeviceTransferExcludeEveryAppStorageDomain() {
        val rules = locateProjectFile("app/src/main/res/xml/data_extraction_rules.xml")
        val document = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(rules)
        val expected = setOf(
            "root",
            "file",
            "database",
            "sharedpref",
            "external",
            "device_root",
            "device_file",
            "device_database",
            "device_sharedpref",
        )

        listOf("cloud-backup", "device-transfer").forEach { sectionName ->
            val section = document.getElementsByTagName(sectionName).item(0)
            assertNotNull(section)
            val exclusions = section.childNodes
            val actual = buildSet {
                for (index in 0 until exclusions.length) {
                    val item = exclusions.item(index)
                    if (item.nodeName == "exclude") {
                        assertEquals(".", item.attributes.getNamedItem("path")?.nodeValue)
                        add(item.attributes.getNamedItem("domain")?.nodeValue)
                    }
                }
            }
            assertEquals(expected, actual)
        }
    }

    @Test
    fun manifestKeepsAllBackupGatesEnabled() {
        val manifest = locateProjectFile("app/src/main/AndroidManifest.xml").readText()
        assertTrue(manifest.contains("android:allowBackup=\"false\""))
        assertTrue(manifest.contains("android:fullBackupContent=\"false\""))
        assertTrue(manifest.contains("android:dataExtractionRules=\"@xml/data_extraction_rules\""))
    }

    private fun locateProjectFile(relative: String): File {
        var directory = File(System.getProperty("user.dir")).canonicalFile
        repeat(8) {
            listOf(
                File(directory, relative),
                File(directory, "mobile/android/$relative"),
            ).firstOrNull(File::isFile)?.let { return it }
            directory = directory.parentFile ?: return@repeat
        }
        throw AssertionError("Android project source file was not found")
    }
}
