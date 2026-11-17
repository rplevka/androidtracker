package org.kidstracker.client

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class EnrollmentQrTest {
    private val code = "0123456789abcdef0123456789abcdef0123456789abcdef"

    @Test
    fun acceptsPlainEnrollmentCodeAndTrimsWhitespace() {
        assertEquals(code, EnrollmentQr.parseCode("  $code\n"))
    }

    @Test
    fun rejectsMalformedEnrollmentCodes() {
        assertThrows(IllegalArgumentException::class.java) {
            EnrollmentQr.parseCode("not-a-token")
        }
    }
}
