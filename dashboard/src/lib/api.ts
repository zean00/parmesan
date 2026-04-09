export type QueryValue = string | number | boolean | undefined;

function buildURL(path: string, params?: Record<string, QueryValue>): URL {
  const url = new URL(path, window.location.origin);
  if (params) {
    for (const [key, value] of Object.entries(params)) {
      if (value === undefined || value === "") continue;
      url.searchParams.set(key, String(value));
    }
  }
  return url;
}

function authHeaders(token: string, base?: HeadersInit): HeadersInit {
  return {
    Accept: "application/json",
    ...(base ?? {}),
    Authorization: `Bearer ${token}`,
  };
}

export async function getJSON<T>(token: string, path: string, params?: Record<string, QueryValue>): Promise<T> {
  const response = await fetch(buildURL(path, params).toString(), {
    headers: authHeaders(token),
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`${response.status} ${response.statusText}: ${text}`);
  }
  return (await response.json()) as T;
}

export async function postJSON<T>(
  token: string,
  path: string,
  body?: unknown,
  params?: Record<string, QueryValue>,
  method = "POST",
): Promise<T> {
  const response = await fetch(buildURL(path, params).toString(), {
    method,
    headers: authHeaders(token, {
      "Content-Type": "application/json",
    }),
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`${response.status} ${response.statusText}: ${text}`);
  }
  return (await response.json()) as T;
}

export async function putJSON<T>(token: string, path: string, body?: unknown, params?: Record<string, QueryValue>): Promise<T> {
  return postJSON<T>(token, path, body, params, "PUT");
}
