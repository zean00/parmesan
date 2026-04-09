export type SSEMessage = {
  event: string;
  id?: string;
  data: string;
};

export async function streamSSE(
  token: string,
  path: string,
  onMessage: (message: SSEMessage) => void,
  signal: AbortSignal,
): Promise<void> {
  const response = await fetch(path, {
    headers: {
      Authorization: `Bearer ${token}`,
      Accept: "text/event-stream",
    },
    signal,
  });
  if (!response.ok || !response.body) {
    throw new Error(`stream failed: ${response.status} ${response.statusText}`);
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  let currentEvent = "message";
  let currentData: string[] = [];
  let currentID = "";

  const flush = () => {
    if (currentData.length === 0) return;
    onMessage({
      event: currentEvent,
      id: currentID || undefined,
      data: currentData.join("\n"),
    });
    currentEvent = "message";
    currentData = [];
    currentID = "";
  };

  while (!signal.aborted) {
    const { done, value } = await reader.read();
    if (done) {
      flush();
      return;
    }
    buffer += decoder.decode(value, { stream: true });
    let splitIndex = buffer.indexOf("\n");
    while (splitIndex >= 0) {
      const rawLine = buffer.slice(0, splitIndex).replace(/\r$/, "");
      buffer = buffer.slice(splitIndex + 1);
      if (rawLine === "") {
        flush();
      } else if (rawLine.startsWith("event:")) {
        currentEvent = rawLine.slice(6).trim();
      } else if (rawLine.startsWith("data:")) {
        currentData.push(rawLine.slice(5).trimStart());
      } else if (rawLine.startsWith("id:")) {
        currentID = rawLine.slice(3).trim();
      }
      splitIndex = buffer.indexOf("\n");
    }
  }
}
