#!/usr/bin/env python3

from os import environ
from smtplib import SMTP


sender = 'trump@whitehouse.gov'
receivers = [f'{environ["PUSHOVER_USER"]}@api.pushover.net']

message = """Subject: Test email

The quick brown fox jumps over the brown lazy dog.
"""

with SMTP('localhost', 1587) as smtp:
    smtp.starttls()
    smtp.ehlo()
    recv_head = ', '.join(f'<{r}>' for r in receivers)
    smtp.sendmail(sender, receivers, f'From: <{sender}>\nTo: {recv_head}\n{message}')
