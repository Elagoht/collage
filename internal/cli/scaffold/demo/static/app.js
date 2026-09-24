function replace(selector, html) {
  const target = document.querySelector(selector);
  if (!target) return;

  const template = document.createElement("template");
  template.innerHTML = html.trim();
  const next = template.content.firstElementChild;
  if (!next) return;

  target.replaceWith(next);
  next.classList.add("swapped");
  next.addEventListener("animationend", () => next.classList.remove("swapped"), { once: true });
}

async function request(url, init) {
  const response = await fetch(url, init);
  if (!response.ok) throw new Error(`${response.status} ${response.statusText}`);
  return response;
}

document.addEventListener("click", async (event) => {
  if (!(event.target instanceof Element)) return;
  const counter = event.target.closest("[data-count]");
  if (counter) {
    const token = document.querySelector('input[name="_csrf"]')?.value ?? "";
    counter.disabled = true;
    try {
      const response = await request(counter.dataset.count, {
        method: "POST",
        headers: { "X-CSRF-Token": token },
      });
      const { count } = await response.json();
      counter.querySelector("output").textContent = String(count);
    } catch (error) {
      console.error("collage: count:", error);
    } finally {
      counter.disabled = false;
    }
    return;
  }
  const fetcher = event.target.closest("[data-fetch]");
  if (fetcher) {
    fetcher.disabled = true;
    try {
      const response = await request(fetcher.dataset.fetch);
      replace(fetcher.dataset.swap, await response.text());
    } catch (error) {
      console.error("collage: fetch fragment:", error);
    } finally {
      fetcher.disabled = false;
    }
    return;
  }
  const json = event.target.closest("[data-json]");
  if (json) {
    const output = document.querySelector(json.dataset.output);
    try {
      const response = await request(json.dataset.json);
      output.textContent = JSON.stringify(await response.json(), null, 2);
    } catch (error) {
      output.textContent = `// ${error.message}`;
    }
  }
});
