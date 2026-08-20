// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

/** Strict RFC-8259 shape scan used because org.json may accept duplicate keys. */
internal class StrictJsonDuplicateKeyScanner(private val source: String) {
    private var index = 0

    fun validate() {
        skipWhitespace()
        parseValue(0)
        skipWhitespace()
        requireJson(index == source.length)
    }

    private fun parseValue(depth: Int) {
        requireJson(depth <= MAX_DEPTH)
        skipWhitespace()
        requireJson(index < source.length)
        when (source[index]) {
            '{' -> parseObject(depth)
            '[' -> parseArray(depth)
            '"' -> parseString()
            't' -> literal("true")
            'f' -> literal("false")
            'n' -> literal("null")
            '-', in '0'..'9' -> parseNumber()
            else -> failJson()
        }
    }

    private fun parseObject(depth: Int) {
        index += 1
        skipWhitespace()
        if (consume('}')) return
        val keys = mutableSetOf<String>()
        while (true) {
            skipWhitespace()
            requireJson(index < source.length && source[index] == '"')
            requireJson(keys.add(parseString()))
            skipWhitespace()
            requireJson(consume(':'))
            parseValue(depth + 1)
            skipWhitespace()
            if (consume('}')) return
            requireJson(consume(','))
        }
    }

    private fun parseArray(depth: Int) {
        index += 1
        skipWhitespace()
        if (consume(']')) return
        while (true) {
            parseValue(depth + 1)
            skipWhitespace()
            if (consume(']')) return
            requireJson(consume(','))
        }
    }

    private fun parseString(): String {
        requireJson(consume('"'))
        val decoded = StringBuilder()
        while (index < source.length) {
            val character = source[index++]
            when {
                character == '"' -> return decoded.toString()
                character == '\\' -> {
                    requireJson(index < source.length)
                    when (val escaped = source[index++]) {
                        '"', '\\', '/' -> decoded.append(escaped)
                        'b' -> decoded.append('\b')
                        'f' -> decoded.append('\u000c')
                        'n' -> decoded.append('\n')
                        'r' -> decoded.append('\r')
                        't' -> decoded.append('\t')
                        'u' -> {
                            requireJson(index + 4 <= source.length)
                            val code = source.substring(index, index + 4).toIntOrNull(16) ?: failJson()
                            decoded.append(code.toChar())
                            index += 4
                        }
                        else -> failJson()
                    }
                }
                character.code < 0x20 -> failJson()
                else -> decoded.append(character)
            }
        }
        failJson()
    }

    private fun parseNumber() {
        consume('-')
        requireJson(index < source.length)
        if (consume('0')) {
            requireJson(index >= source.length || source[index] !in '0'..'9')
        } else {
            requireJson(index < source.length && source[index] in '1'..'9')
            while (index < source.length && source[index] in '0'..'9') index += 1
        }
        if (consume('.')) {
            requireJson(index < source.length && source[index] in '0'..'9')
            while (index < source.length && source[index] in '0'..'9') index += 1
        }
        if (index < source.length && source[index] in "eE") {
            index += 1
            if (index < source.length && source[index] in "+-") index += 1
            requireJson(index < source.length && source[index] in '0'..'9')
            while (index < source.length && source[index] in '0'..'9') index += 1
        }
    }

    private fun literal(value: String) {
        requireJson(source.regionMatches(index, value, 0, value.length))
        index += value.length
    }

    private fun skipWhitespace() {
        while (index < source.length && source[index] in " \t\r\n") index += 1
    }

    private fun consume(character: Char): Boolean {
        if (index >= source.length || source[index] != character) return false
        index += 1
        return true
    }

    private fun requireJson(condition: Boolean) {
        if (!condition) failJson()
    }

    private fun failJson(): Nothing = throw IllegalArgumentException("invalid JSON envelope")

    private companion object {
        const val MAX_DEPTH = 64
    }
}
