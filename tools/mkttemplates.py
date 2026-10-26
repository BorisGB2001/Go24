#!/usr/bin/env python3
"""Ready-made letters of «Маркетинг → Шаблоны» (internal/web/mkttemplates/<lang>/<kind>.html) from one layout:
tables and inline styles only, as mail clients and mail.Sanitize keep them. Run after changing a text or the layout."""
import os
D=os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "internal", "web", "mkttemplates")
W='<table width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:{bg};font-family:Arial, Helvetica, sans-serif"><tbody><tr><td align="center" style="padding:24px 12px"><table width="600" cellpadding="0" cellspacing="0" border="0" style="max-width:600px;width:100%;background-color:#ffffff;border-radius:8px"><tbody>{rows}</tbody></table></td></tr></tbody></table>'
def head(c,t,s=""): return f'<tr><td style="background-color:{c};padding:28px 32px;border-radius:8px 8px 0 0;color:#ffffff"><h1 style="margin:0;font-size:26px;line-height:1.3;color:#ffffff">{t}</h1>'+(f'<p style="margin:8px 0 0;font-size:15px;color:#ffffff">{s}</p>' if s else '')+'</td></tr>'
def p(t): return f'<tr><td style="padding:8px 32px;font-size:15px;line-height:1.6;color:#333333"><p style="margin:0">{t}</p></td></tr>'
def h2(t): return f'<tr><td style="padding:20px 32px 0"><h2 style="margin:0;font-size:19px;color:#222222">{t}</h2></td></tr>'
def btn(c,t): return f'<tr><td align="center" style="padding:24px 32px"><a href="https://example.com" style="display:inline-block;background-color:{c};color:#ffffff;text-decoration:none;font-size:16px;font-weight:bold;padding:12px 28px;border-radius:6px">{t}</a></td></tr>'
def hr(): return '<tr><td style="padding:16px 32px"><hr style="border:0;border-top:1px solid #e5e7eb;margin:0"></td></tr>'
def foot(t): return f'<tr><td style="padding:16px 32px 24px;font-size:12px;line-height:1.5;color:#888888;border-top:1px solid #eeeeee">{t}</td></tr>'
def big(c,t,s): return f'<tr><td align="center" style="padding:28px 32px 8px"><div style="font-size:44px;font-weight:bold;color:{c}">{t}</div><div style="font-size:15px;color:#555555">{s}</div></td></tr>'
def info(rows): return '<tr><td style="padding:12px 32px"><table width="100%" cellpadding="8" cellspacing="0" border="0" style="background-color:#f5f7fa;border-radius:6px;font-size:15px;color:#333333"><tbody>'+''.join(f'<tr><td style="width:30%;color:#777777">{a}</td><td><b>{b}</b></td></tr>' for a,b in rows)+'</tbody></table></td></tr>'
def item(t,x,more): return f'<tr><td style="padding:12px 32px;border-bottom:1px solid #f0f0f0"><h3 style="margin:0 0 6px;font-size:17px;color:#222222">{t}</h3><p style="margin:0 0 6px;font-size:14px;line-height:1.5;color:#555555">{x}</p><a href="https://example.com" style="font-size:14px;color:#2f6fed">{more}</a></td></tr>'
L={
"ru-RU":dict(hello="Здравствуйте, {Name}!",more="Подробнее →",
 foot="Вы получили это письмо, потому что являетесь клиентом нашей компании. Компания · Адрес · Телефон",
 simple=[p("Здравствуйте, {Name}!"),p("Текст письма. Расскажите, зачем вы пишете, коротко и по делу."),p("С уважением,<br>Имя Фамилия<br>Компания")],
 news=["Новости компании","Что нового за месяц",["Главная новость","Пара предложений о самом важном событии месяца."],["Новый продукт","Что появилось и чем это полезно клиенту."],["Мы в цифрах","Достижения, отзывы клиентов, планы."]],
 promo=["Специальное предложение","Только до конца месяца","−20%","на все товары каталога","Скидка действует по промокоду <b>SALE20</b> при заказе на сайте или у вашего менеджера.","Выбрать со скидкой"],
 event=["Приглашаем на мероприятие","Название мероприятия","Будем рады видеть вас на нашем мероприятии. Расскажем о новинках, ответим на вопросы и поделимся опытом.",[("Дата","1 декабря, 18:00"),("Место","Город, улица, дом"),("Участие","Бесплатно, по регистрации")],"Зарегистрироваться"],
 digest=["Дайджест","Самое интересное за неделю",[("Статья недели","Короткий анонс статьи: о чём она и почему её стоит прочитать."),("Полезный совет","Как получить больше от нашего продукта."),("Кейс клиента","Как клиент решил свою задачу с нашей помощью.")]],
),
"en-US":dict(hello="Hello, {Name}!",more="Read more →",
 foot="You receive this letter as a customer of our company. Company · Address · Phone",
 simple=[p("Hello, {Name}!"),p("The text of the letter. Say briefly why you are writing."),p("Best regards,<br>First Last<br>Company")],
 news=["Company news","What's new this month",["Top story","A couple of sentences about the main event of the month."],["New product","What's new and how it helps the customer."],["In numbers","Achievements, customer reviews, plans."]],
 promo=["Special offer","Until the end of the month only","−20%","on the whole catalogue","Use the promo code <b>SALE20</b> on the website or with your manager.","Shop the sale"],
 event=["You are invited","Event title","We'll be glad to see you at our event: news, answers to your questions and experience to share.",[("Date","December 1, 6 PM"),("Place","City, street, building"),("Admission","Free, registration required")],"Register"],
 digest=["Digest","The best of the week",[("Article of the week","A short teaser: what it is about and why it's worth reading."),("Useful tip","How to get more out of our product."),("Customer story","How a customer solved their task with our help.")]],
)}
for lang,t in L.items():
    out={}
    out["simple"]=W.format(bg="#f4f5f7",rows=''.join(t["simple"]).replace('padding:8px 32px','padding:12px 32px',1)+foot(t["foot"]))
    n=t["news"]; out["news"]=W.format(bg="#eef2f7",rows=head("#2f6fed",n[0],n[1])+p(t["hello"])+''.join(item(a,b,t["more"]) for a,b in n[2:])+foot(t["foot"]))
    m=t["promo"]; out["promo"]=W.format(bg="#fff4ec",rows=head("#f06a2a",m[0],m[1])+big("#f06a2a",m[2],m[3])+p(t["hello"])+p(m[4])+btn("#f06a2a",m[5])+foot(t["foot"]))
    e=t["event"]; out["event"]=W.format(bg="#f1f0fb",rows=head("#6a4fd8",e[0],e[1])+p(t["hello"])+p(e[2])+info(e[3])+btn("#6a4fd8",e[4])+foot(t["foot"]))
    g=t["digest"]; out["digest"]=W.format(bg="#eef7f1",rows=head("#1f9d55",g[0],g[1])+p(t["hello"])+''.join(item(a,b,t["more"]) for a,b in g[2])+foot(t["foot"]))
    for k,v in out.items(): open(f"{D}/{lang}/{k}.html","w").write(v+"\n")
