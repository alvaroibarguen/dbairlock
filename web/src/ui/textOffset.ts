/**
 * Converts a byte offset into UTF-8 text (the server reports error
 * positions in bytes) to a JavaScript string index (UTF-16 code units).
 */
export function byteToIndex(text: string, byteOffset: number): number {
  if (byteOffset <= 0) return 0
  const encoder = new TextEncoder()
  let bytes = 0
  let i = 0
  for (const ch of text) {
    const n = encoder.encode(ch).length
    if (bytes + n > byteOffset) break
    bytes += n
    i += ch.length
  }
  return i
}
