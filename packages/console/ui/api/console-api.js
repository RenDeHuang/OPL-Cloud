export async function postJson(path, body = {}, csrfToken = "") {
  const headers = {
    "content-type": "application/json"
  };
  if (csrfToken) headers["x-opl-csrf"] = csrfToken;
  const response = await fetch(path, {
    method: "POST",
    headers,
    body: JSON.stringify(body)
  });
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "request_failed");
  return payload;
}

export const api = postJson;

export async function getJson(path) {
  const response = await fetch(path);
  const payload = await response.json();
  if (!response.ok || payload.ok === false) throw new Error(payload.error || "request_failed");
  return payload;
}
