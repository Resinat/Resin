import type { AppLocale } from "./locale";
import { translate } from "./translations";

const TRANSLATABLE_ATTRIBUTES = ["placeholder", "title", "aria-label"] as const;

type TextState = {
  source: string;
  lastRendered: string;
};

type AttributeState = {
  source: string;
  lastRendered: string;
};

const textStateByNode = new WeakMap<Text, TextState>();
const attributeStateByElement = new WeakMap<Element, Map<string, AttributeState>>();

function shouldSkipElement(element: Element): boolean {
  const tag = element.tagName;
  return tag === "SCRIPT" || tag === "STYLE" || tag === "PRE" || tag === "CODE";
}

function getElementAttributeStates(element: Element): Map<string, AttributeState> {
  let attrs = attributeStateByElement.get(element);
  if (attrs) {
    return attrs;
  }
  attrs = new Map<string, AttributeState>();
  attributeStateByElement.set(element, attrs);
  return attrs;
}

function applyTextNode(node: Text, locale: AppLocale) {
  const current = node.nodeValue ?? "";
  const previous = textStateByNode.get(node);

  let source = current;
  if (previous) {
    source = previous.source;
    if (current !== previous.lastRendered) {
      source = current;
    }
  }

  const next = locale === "en-US" ? translate(locale, source) : source;
  if (next !== current) {
    node.nodeValue = next;
  }
  textStateByNode.set(node, {
    source,
    lastRendered: next,
  });
}

function applyAttributes(element: Element, locale: AppLocale) {
  const attributeStates = getElementAttributeStates(element);

  for (const attr of TRANSLATABLE_ATTRIBUTES) {
    if (!element.hasAttribute(attr)) {
      continue;
    }

    const current = element.getAttribute(attr) ?? "";
    const previous = attributeStates.get(attr);

    let source = current;
    if (previous) {
      source = previous.source;
      if (current !== previous.lastRendered) {
        source = current;
      }
    }

    const next = locale === "en-US" ? translate(locale, source) : source;
    if (next !== current) {
      element.setAttribute(attr, next);
    }

    attributeStates.set(attr, {
      source,
      lastRendered: next,
    });
  }
}

function applyNodeTree(node: Node, locale: AppLocale) {
  if (node.nodeType === Node.TEXT_NODE) {
    applyTextNode(node as Text, locale);
    return;
  }

  if (!(node instanceof Element)) {
    return;
  }

  if (shouldSkipElement(node)) {
    return;
  }

  applyAttributes(node, locale);

  // Avoid touching textarea content value text nodes; only translate attributes.
  if (node.tagName === "TEXTAREA") {
    return;
  }

  for (const child of Array.from(node.childNodes)) {
    applyNodeTree(child, locale);
  }
}

export function attachLegacyDomTranslation(locale: AppLocale): () => void {
  if (typeof window === "undefined" || !document.body) {
    return () => {};
  }

  let applying = false;
  const apply = (node: Node) => {
    if (applying) {
      return;
    }
    applying = true;
    try {
      applyNodeTree(node, locale);
    } finally {
      applying = false;
    }
  };

  apply(document.body);

  const observer = new MutationObserver((mutations) => {
    if (applying) {
      return;
    }

    for (const mutation of mutations) {
      if (mutation.type === "characterData") {
        apply(mutation.target);
        continue;
      }

      if (mutation.type === "attributes") {
        apply(mutation.target);
        continue;
      }

      for (const node of Array.from(mutation.addedNodes)) {
        apply(node);
      }
    }
  });

  observer.observe(document.body, {
    childList: true,
    subtree: true,
    characterData: true,
    attributes: true,
    attributeFilter: [...TRANSLATABLE_ATTRIBUTES],
  });

  return () => observer.disconnect();
}
