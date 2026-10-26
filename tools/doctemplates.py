#!/usr/bin/env python3
"""Builds the ready-made templates of «Генератор документов» (GO24DEV-519): an invoice, a contract and an act of
a deal, in each language, into internal/web/doctemplates/<lang>/. The fields are those of the templates page
({MyCompany.…}, {Client.…}, a products row {Product.…}, {Total}…). Plain WordprocessingML, no libraries:

    python3 tools/doctemplates.py
"""
import os
import zipfile
from xml.sax.saxutils import escape

OUT = os.path.join(os.path.dirname(__file__), "..", "internal", "web", "doctemplates")

CONTENT_TYPES = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>"""

RELS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>"""

DOC_RELS = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>"""

STYLES = """<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Arial" w:hAnsi="Arial" w:cs="Arial" w:eastAsia="Arial"/><w:sz w:val="20"/><w:szCs w:val="20"/><w:lang w:val="%s"/></w:rPr></w:rPrDefault>
<w:pPrDefault><w:pPr><w:spacing w:after="80" w:line="240" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
<w:style w:type="table" w:default="1" w:styleId="TableNormal"><w:name w:val="Normal Table"/><w:tblPr><w:tblCellMar><w:left w:w="80" w:type="dxa"/><w:right w:w="80" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>
</w:styles>"""

W = 'xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"'
PAGE = 9638  # A4 minus 2 cm and 1.5 cm margins, in twips


def run(text, bold=False, size=None):
    pr = ("<w:b/>" if bold else "") + (f'<w:sz w:val="{size}"/><w:szCs w:val="{size}"/>' if size else "")
    return f'<w:r>{"<w:rPr>" + pr + "</w:rPr>" if pr else ""}<w:t xml:space="preserve">{escape(text)}</w:t></w:r>'


def p(text="", bold=False, size=None, align=None, after=None):
    """A paragraph; text may be a list of (text, bold) parts."""
    parts = text if isinstance(text, list) else [(text, bold)]
    pr = (f'<w:jc w:val="{align}"/>' if align else "") + (f'<w:spacing w:after="{after}"/>' if after is not None else "")
    return f'<w:p>{"<w:pPr>" + pr + "</w:pPr>" if pr else ""}{"".join(run(t, b, size) for t, b in parts)}</w:p>'


def table(rows, widths, borders=True, bold_rows=(), aligns=None):
    """rows of cells (a cell: text, or a list of paragraphs' xml); aligns: per column (None, "right", "center")."""
    b = "".join(f'<w:{s} w:val="{"single" if borders else "nil"}" w:sz="4" w:space="0" w:color="000000"/>'
                for s in ("top", "left", "bottom", "right", "insideH", "insideV"))
    x = f'<w:tbl><w:tblPr><w:tblW w:w="{sum(widths)}" w:type="dxa"/><w:tblBorders>{b}</w:tblBorders><w:tblLayout w:type="fixed"/></w:tblPr>'
    x += "<w:tblGrid>" + "".join(f'<w:gridCol w:w="{w}"/>' for w in widths) + "</w:tblGrid>"
    for i, row in enumerate(rows):
        x += "<w:tr>"
        for j, cell in enumerate(row):
            body = "".join(cell) if isinstance(cell, list) else p(cell, bold=i in bold_rows, align=(aligns or [None] * len(row))[j], after=0)
            x += f'<w:tc><w:tcPr><w:tcW w:w="{widths[j]}" w:type="dxa"/></w:tcPr>{body}</w:tc>'
        x += "</w:tr>"
    return x + "</w:tbl>"


def products(t, name):
    head = ["№", name, t["qty"], t["unit"], t["price"], t["sum"]]
    row = ["{Product.Number}", "{Product.Name}", "{Product.Quantity}", "{Product.Unit}", "{Product.Price}", "{Product.Sum}"]
    widths = [500, 4538, 1000, 800, 1400, 1400]
    return table([head, row], widths, bold_rows=(0,), aligns=["center", None, "right", "center", "right", "right"])


def totals(t):
    w = [PAGE - 1400, 1400]
    return table([[t["total_net"], "{TotalNet}"], [t["tax_total"], "{TaxTotal}"], [t["total"], "{Total}"]], w, borders=False,
                 bold_rows=(0, 1, 2), aligns=["right", "right"])


def signs(left, right):
    w = [PAGE // 2, PAGE - PAGE // 2]
    return table([[[p(left[0], bold=True), p(left[1]), p("_______________ " + left[2])],
                   [p(right[0], bold=True), p(right[1]), p("_______________ " + right[2])]]], w, borders=False)


def seller(t):
    return "{MyCompany.FullName}, " + t["inn"] + " {MyCompany.INN}, " + t["kpp"] + " {MyCompany.KPP}, {MyCompany.Address}"


def invoice(t):
    bank = table([
        [[p("{MyCompany.Bank}", after=0), p(t["payee_bank"], size=16, after=0)], t["bik"], "{MyCompany.BIK}"],
        ["", t["acc_no"], "{MyCompany.CorrAccount}"],
        [t["inn"] + " {MyCompany.INN}    " + t["kpp"] + " {MyCompany.KPP}", t["acc_no"], "{MyCompany.Account}"],
        [[p("{MyCompany.FullName}", after=0), p(t["payee"], size=16, after=0)], "", ""],
    ], [5338, 1100, 3200])
    return (bank + p("") + p(t["invoice_title"] + " № {DocumentNumber} " + t["of"] + " {DocumentDate}", bold=True, size=28, after=200)
            + p([(t["supplier"] + ": ", False), (seller(t), True)])
            + p([(t["buyer"] + ": ", False), ("{Client.FullName}, " + t["inn"] + " {Client.INN}, " + t["kpp"] + " {Client.KPP}, {Client.Address}", True)])
            + p([(t["basis"] + ": ", False), ("{Title}", True)], after=200)
            + products(t, t["goods"]) + totals(t)
            + p(t["items_total"] + " {ProductsCount}, " + t["amount_of"] + " {Total} {Currency}")
            + p(t["pay_note"], size=16, after=400)
            + table([[t["director"], "_______________", "{MyCompany.Director}"]], [3000, 3000, 3638], borders=False))


def contract(t):
    s = t["c"]
    x = p(s["title"] + " № {DocumentNumber}", bold=True, size=28, align="center")
    x += table([[s["city"], "{DocumentDate}"]], [PAGE // 2, PAGE - PAGE // 2], borders=False, aligns=[None, "right"])
    x += p(s["parties"], after=160)
    for title, body in s["sections"]:
        x += p(title, bold=True, after=60)
        for line in body:
            if line == "@products":
                x += products(t, t["goods"]) + totals(t)
            else:
                x += p(line)
    x += p(s["details"], bold=True, after=60)

    def party(role, pre):
        return [p(role, bold=True)] + [p(f"{label}: {{{pre}.{f}}}", after=0) for label, f in s["fields"]] + [
            p(""), p("_______________ {" + pre + ".Director}")]
    x += table([[party(s["seller"], "MyCompany"), party(s["buyer"], "Client")]], [PAGE // 2, PAGE - PAGE // 2], borders=False)
    return x


def act(t):
    a = t["a"]
    return (p(a["title"] + " № {DocumentNumber} " + t["of"] + " {DocumentDate}", bold=True, size=28, after=200)
            + p([(a["seller"] + ": ", False), (seller(t), True)])
            + p([(a["buyer"] + ": ", False), ("{Client.FullName}, " + t["inn"] + " {Client.INN}, {Client.Address}", True)])
            + p([(t["basis"] + ": ", False), ("{Title}", True)], after=200)
            + products(t, a["works"]) + totals(t)
            + p(a["count"] + " {ProductsCount}, " + t["amount_of"] + " {Total} {Currency}")
            + p(a["done"], after=400)
            + signs((a["seller"], "{MyCompany.Title}", "{MyCompany.Director}"), (a["buyer"], "{Client.Title}", "{Client.Director}")))


RU = {
    "lang": "ru-RU", "of": "от", "inn": "ИНН", "kpp": "КПП", "bik": "БИК", "acc_no": "Сч. №",
    "payee_bank": "Банк получателя", "payee": "Получатель", "invoice_title": "Счёт на оплату",
    "supplier": "Поставщик", "buyer": "Покупатель", "basis": "Основание", "goods": "Товары (работы, услуги)",
    "qty": "Кол-во", "unit": "Ед.", "price": "Цена", "sum": "Сумма",
    "total_net": "Итого без НДС:", "tax_total": "В том числе НДС:", "total": "Всего к оплате:",
    "items_total": "Всего наименований", "amount_of": "на сумму",
    "pay_note": "Оплата данного счёта означает согласие с условиями поставки товара.", "director": "Руководитель",
    "c": {
        "title": "Договор", "city": "г. ____________",
        "parties": "{MyCompany.FullName}, именуемое в дальнейшем «Исполнитель», в лице {MyCompany.Director}, с одной стороны, "
                   "и {Client.FullName}, именуемое в дальнейшем «Заказчик», с другой стороны, заключили настоящий договор о нижеследующем.",
        "sections": [
            ("1. Предмет договора", ["1.1. Исполнитель обязуется передать Заказчику товары (выполнить работы, оказать услуги), "
                                     "указанные ниже, а Заказчик — принять и оплатить их.", "@products"]),
            ("2. Цена и порядок расчётов", ["2.1. Цена договора — {Total} {Currency}, в том числе НДС {TaxTotal}.",
                                           "2.2. Заказчик оплачивает счёт Исполнителя в течение 5 (пяти) банковских дней с даты его получения."]),
            ("3. Сроки", ["3.1. Исполнитель исполняет обязательства до {CloseDate}."]),
            ("4. Ответственность сторон", ["4.1. За неисполнение или ненадлежащее исполнение обязательств стороны несут ответственность "
                                          "в соответствии с законодательством Российской Федерации."]),
            ("5. Прочие условия", ["5.1. Договор вступает в силу с даты подписания и действует до полного исполнения сторонами обязательств.",
                                  "5.2. Договор составлен в двух экземплярах, по одному для каждой из сторон."]),
        ],
        "details": "6. Адреса и реквизиты сторон", "seller": "Исполнитель", "buyer": "Заказчик",
        "fields": [("Наименование", "FullName"), ("ИНН", "INN"), ("КПП", "KPP"), ("ОГРН", "OGRN"), ("Адрес", "Address"),
                   ("Банк", "Bank"), ("БИК", "BIK"), ("Р/с", "Account"), ("К/с", "CorrAccount"), ("Телефон", "Phone")],
    },
    "a": {
        "title": "Акт", "seller": "Исполнитель", "buyer": "Заказчик", "works": "Наименование работ, услуг",
        "count": "Всего оказано услуг", "done": "Вышеперечисленные услуги выполнены полностью и в срок. "
                                                "Заказчик претензий по объёму, качеству и срокам оказания услуг не имеет.",
    },
}

EN = {
    "lang": "en-US", "of": "of", "inn": "Tax ID", "kpp": "KPP", "bik": "BIK", "acc_no": "Account",
    "payee_bank": "Payee's bank", "payee": "Payee", "invoice_title": "Invoice",
    "supplier": "Supplier", "buyer": "Buyer", "basis": "Reference", "goods": "Goods (works, services)",
    "qty": "Qty", "unit": "Unit", "price": "Price", "sum": "Amount",
    "total_net": "Total excl. VAT:", "tax_total": "VAT:", "total": "Total due:",
    "items_total": "Items", "amount_of": "total",
    "pay_note": "Payment of this invoice means acceptance of the terms of delivery.", "director": "Director",
    "c": {
        "title": "Agreement", "city": "____________",
        "parties": "{MyCompany.FullName} (the «Contractor»), represented by {MyCompany.Director}, and {Client.FullName} "
                   "(the «Customer») have agreed as follows.",
        "sections": [
            ("1. Subject", ["1.1. The Contractor shall deliver to the Customer the goods (works, services) listed below, "
                            "and the Customer shall accept and pay for them.", "@products"]),
            ("2. Price and payment", ["2.1. The price of the agreement is {Total} {Currency}, including VAT {TaxTotal}.",
                                     "2.2. The Customer pays the Contractor's invoice within 5 (five) business days of receiving it."]),
            ("3. Terms", ["3.1. The Contractor fulfils its obligations by {CloseDate}."]),
            ("4. Liability", ["4.1. The parties are liable for failure to perform their obligations in accordance with the applicable law."]),
            ("5. Other terms", ["5.1. The agreement comes into force when signed and is valid until the parties fulfil their obligations.",
                               "5.2. The agreement is made in two copies, one for each party."]),
        ],
        "details": "6. Addresses and details of the parties", "seller": "Contractor", "buyer": "Customer",
        "fields": [("Name", "FullName"), ("Tax ID", "INN"), ("Address", "Address"), ("Bank", "Bank"), ("BIK", "BIK"),
                   ("Account", "Account"), ("Phone", "Phone"), ("E-mail", "Email")],
    },
    "a": {
        "title": "Certificate of completion", "seller": "Contractor", "buyer": "Customer", "works": "Works, services",
        "count": "Services rendered", "done": "The services above have been rendered in full and on time. "
                                              "The Customer has no claims as to their scope, quality and terms.",
    },
}


def write(path, lang, body):
    doc = (f'<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n<w:document {W}><w:body>{body}'
           '<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="850" w:bottom="1134" w:left="1418" '
           'w:header="709" w:footer="709" w:gutter="0"/></w:sectPr></w:body></w:document>')
    with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as z:
        for name, data in (("[Content_Types].xml", CONTENT_TYPES), ("_rels/.rels", RELS),
                           ("word/_rels/document.xml.rels", DOC_RELS), ("word/styles.xml", STYLES % lang),
                           ("word/document.xml", doc)):
            info = zipfile.ZipInfo(name, (2026, 1, 1, 0, 0, 0))  # the same bytes on each run
            info.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(info, data)


for t in (RU, EN):
    d = os.path.join(OUT, t["lang"])
    os.makedirs(d, exist_ok=True)
    for kind, make in (("invoice", invoice), ("contract", contract), ("act", act)):
        write(os.path.join(d, kind + ".docx"), t["lang"], make(t))
