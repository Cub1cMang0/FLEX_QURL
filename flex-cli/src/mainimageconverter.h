#ifndef MAINIMAGECONVERTER_H
#define MAINIMAGECONVERTER_H

#include <QImage>
#include "imagefileconverter.h"

class MainImageConverter: public QObject, public ImageFileConverter
{
    Q_OBJECT
    public:
        explicit MainImageConverter(QObject *parent = nullptr);
        void convert_image(const QString &input_path, const QString &output_path, const QString &input_ext, const QString &output_ext);
    signals:
        void update_image_progress(const QString &message, bool success);
};

#endif
