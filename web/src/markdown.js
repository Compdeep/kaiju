import { marked } from 'marked'
import DOMPurify from 'dompurify'

/**
 * Turning a model's reply into HTML, safely.
 *
 * A reply is markdown, and markdown carries raw HTML through by design — marked
 * does not strip it and has not offered to since version 5. So whatever the
 * model writes lands in the page as live elements, and a model's wording is
 * shaped by what it read: a fetched web page, an extracted attachment, host
 * telemetry. A page that tells the model to answer with an image tag carrying an
 * onerror attribute gets script running on this origin, where the sign-in token
 * is readable and every route is reachable as whoever is signed in.
 *
 * So the output is sanitised before it reaches the page. Sanitising the output
 * rather than escaping the input is what keeps the formatting: tables, code
 * blocks, links and emphasis all still render, and only the constructs that can
 * execute are removed.
 *
 * The application's own dashboard already does the equivalent — its formatVerdict
 * escapes the three characters before adding markup — which is why this was the
 * only surface with the hole.
 */

// What survives. Everything markdown produces, and nothing that runs.
//
// Written out rather than left to the default so that widening it is a decision
// somebody makes on purpose. No `script`, no `style`, no `iframe`, no `object`,
// no `embed`, no `form`, and no event-handler attributes of any kind.
const ALLOWED_TAGS = [
  'p', 'br', 'hr', 'span', 'div',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'strong', 'em', 'b', 'i', 'u', 's', 'del', 'ins', 'mark', 'sub', 'sup',
  'ul', 'ol', 'li',
  'blockquote', 'pre', 'code', 'kbd', 'samp', 'var',
  'a', 'img',
  'table', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td', 'caption', 'colgroup', 'col',
  'details', 'summary', 'dl', 'dt', 'dd',
]

// Attributes that carry no behaviour. `class` is here because the code
// highlighter marks up its output with it, and dropping it would leave every
// code block unstyled.
const ALLOWED_ATTR = ['href', 'src', 'alt', 'title', 'class', 'width', 'height', 'align', 'colspan', 'rowspan', 'start', 'open', 'lang', 'dir']

const CONFIG = {
  ALLOWED_TAGS,
  ALLOWED_ATTR,
  // A link or an image may name only these ways of fetching. Without this a
  // javascript: address in an href would survive, since href is allowed.
  ALLOWED_URI_REGEXP: /^(?:https?|mailto|tel|data:image\/(?:png|jpe?g|gif|webp|svg\+xml);base64,):/i,
  // Nothing outside the fragment: no <html>, <head> or <body> wrapper is added,
  // and none is accepted.
  WHOLE_DOCUMENT: false,
  // Return a string, which is what v-html takes.
  RETURN_DOM: false,
  RETURN_DOM_FRAGMENT: false,
}

/**
 * desc: Render markdown to HTML that is safe to put in the page.
 * @param {string} text - the reply, as markdown
 * @returns {string} HTML with every executable construct removed
 */
export function renderMarkdown(text) {
  if (!text) return ''
  return DOMPurify.sanitize(marked.parse(text), CONFIG)
}
