"""Checks for webreader's page extraction.

Run with the plugin host's Python, which has trafilatura:

    cd plugins/webreader && ~/.local/share/kaiju/plugin-host/venv/bin/python3 -m unittest test_plugin
"""
import importlib.util
import os
import unittest

_spec = importlib.util.spec_from_file_location(
    "webreader_plugin", os.path.join(os.path.dirname(__file__), "plugin.py"))
plugin = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(plugin)

_FILLER = "".join(
    "<p>Filler paragraph %d with enough ordinary words so that the extractor keeps the article body intact.</p>" % i
    for i in range(6))


def _page(middle: str) -> str:
    return "<html><body><article><h1>Manual</h1>" + _FILLER + middle + _FILLER + "</article></body></html>"


@unittest.skipIf(plugin.trafilatura is None, "trafilatura is not installed")
class ExtractTest(unittest.TestCase):
    # The Horizons manual's line naming the barycentre's codes, as the page
    # writes it. trafilatura alone returns "0 andssb refer to".
    def test_inline_code_keeps_its_spaces(self):
        text = plugin._extract(_page(
            "<ul><li><p><code>0</code> and <code>ssb</code> refer to the solar system barycenter (SSB).</p></li>"
            "<li><p><code>10</code> and <code>sun</code> refer to the center of the Sun.</p></li></ul>"))
        self.assertIn("`0` and `ssb` refer to the solar system barycenter", text)
        self.assertIn("`10` and `sun` refer to the center of the Sun", text)

    # A list item that is one code element, as the Horizons API page's example
    # request is, survives.
    def test_code_alone_in_an_item_is_kept(self):
        url = "https://ssd.jpl.nasa.gov/api/horizons.api?format=text&amp;COMMAND='499'&amp;CENTER='500@399'"
        text = plugin._extract(_page("<p>Request an observer ephemeris of Mars:</p><ul><li><code>" + url + "</code></li></ul>"))
        self.assertIn("CENTER='500@399'", text)

    # A <pre> block is left as written.
    def test_pre_block_is_untouched(self):
        html = "<pre><code>x = 1\ny = <b>2</b></code></pre>"
        self.assertEqual(plugin._unwrap_code_in_text(html), html)

    def test_tables_are_kept(self):
        text = plugin._extract(_page(
            "<table><tr><th>Body</th><th>r1 (km)</th></tr><tr><td>Jupiter</td><td>742,370</td></tr></table>"))
        self.assertIn("742,370", text)


if __name__ == "__main__":
    unittest.main()
