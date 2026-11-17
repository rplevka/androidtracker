package org.kidstracker.client

import java.io.ByteArrayOutputStream
import java.util.zip.GZIPOutputStream

object LocationBatchEncoder {
    fun gzip(payload: ByteArray): ByteArray {
        val output = ByteArrayOutputStream()
        GZIPOutputStream(output).use { it.write(payload) }
        return output.toByteArray()
    }
}
