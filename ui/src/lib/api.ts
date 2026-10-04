// apiError extracts the server's {"error": "..."} message from a failed response.
export async function apiError(res: Response): Promise<string> {
  const text = await res.text()
  try {
    const parsed = JSON.parse(text)
    if (parsed && typeof parsed.error === 'string') return parsed.error
  } catch {
    // Not JSON: fall through to the raw body.
  }
  return text || res.statusText
}
