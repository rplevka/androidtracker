package org.kidstracker.client

object EnrollmentQr {
    fun parseCode(contents: String): String {
        val code = contents.trim()
        require(code.length in 20..256 && code.all { it in '0'..'9' || it.lowercaseChar() in 'a'..'f' }) {
            "QR code must contain a valid enrollment code."
        }
        return code
    }
}
